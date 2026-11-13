// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This file is part of Kyro.
//
// Kyro is free software: you can redistribute it and/or modify it under the
// terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version.
//
// Kyro is distributed in the hope that it will be useful, but WITHOUT ANY
// WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
// FOR A PARTICULAR PURPOSE. See the GNU General Public License for more
// details.
//
// You should have received a copy of the GNU General Public License along
// with Kyro. If not, see <https://www.gnu.org/licenses/>.

// Package boot is the HTTP side of Kyro: what iPXE, Windows PE and the
// installed system ask for.
//
// It has two handlers. The boot handler faces the boot network, which is
// untrusted; it offers the boot scripts, the files of an installation in
// progress and the callback. The status handler is read-only and is meant
// for a trusted address.
package boot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/state"
)

// Service answers the HTTP requests of clients.
type Service struct {
	Config   config.Config
	State    *state.Store
	Images   *images.Tree
	Profiles *profiles.Store
	// Wimboot is the content of the wimboot binary.
	Wimboot []byte
	Log     *slog.Logger
	// OnInstall, if set, is called with the address of a client that has
	// just been given the installer.
	OnInstall func(addr netip.Addr)

	boots     *rateLimit
	callbacks *rateLimit
}

// maxQueryValue bounds each identifier in a boot request.
const maxQueryValue = 128

// Handler returns the handler for the boot network.
func (s *Service) Handler() http.Handler {
	// Booting is rare; a client asks a handful of times per installation.
	s.boots = newRateLimit(30, 0.5)
	s.callbacks = newRateLimit(20, 0.2)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boot.ipxe", s.handleBootstrap)
	mux.HandleFunc("GET /boot", s.handleBoot)
	mux.HandleFunc("GET /wimboot", s.handleWimboot)
	mux.HandleFunc("GET /i/{token}/{file}", s.handleInstallFile)
	mux.HandleFunc("POST /api/v1/callback/{token}", s.handleCallback)
	return mux
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr, _ := netip.ParseAddr(host)
	return addr.Unmap()
}

func script(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, body)
}

func (s *Service) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	script(w, bootstrapScript(s.Config.HTTPBase()))
}

func (s *Service) handleWimboot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(s.Wimboot))
}

// handleBoot decides what a machine boots. This is where an installation
// starts, and the only place.
func (s *Service) handleBoot(w http.ResponseWriter, r *http.Request) {
	addr := remoteAddr(r)
	if !s.boots.allow(addr) {
		// Too many requests: the safe answer is the local disk.
		script(w, localScript("too many boot requests from this address."))
		return
	}
	q := r.URL.Query()
	for _, key := range []string{"mac", "uuid", "serial"} {
		if len(q.Get(key)) > maxQueryValue {
			script(w, localScript("the boot request is malformed."))
			return
		}
	}
	id := state.NewIdentity(q.Get("mac"), q.Get("uuid"), q.Get("serial"))

	d, err := s.State.Boot(id, addr.String())
	if err != nil {
		// Without a state that can be read and written nobody gets an
		// installer.
		s.Log.Error("boot: cannot read or write the client state", "err", err)
		script(w, localScript("the Kyro server has a problem with its client records."))
		return
	}
	if !d.Install {
		s.Log.Info("boot: local disk", "client", d.Client.Key.String(), "state", string(d.Client.State), "mac", id.MAC, "addr", addr.String())
		if (d.Client.State == state.Unknown || d.Client.Key == (state.Key{})) && s.Config.UnknownClients == "menu" {
			script(w, menuScript())
			return
		}
		why := "this machine is not armed for an installation."
		switch d.Client.State {
		case state.Installing:
			why = "the installation of this machine is under way."
		case state.Provisioned:
			why = "this machine is installed."
		case state.Failed:
			why = "the last installation of this machine failed; arm it again to retry."
		}
		script(w, localScript(why))
		return
	}

	// The client is now "installing", whatever happens next. If the
	// installer cannot be put together, that is a failed installation.
	c := d.Client
	body, err := s.prepare(c)
	if err != nil {
		s.Log.Error("boot: cannot start the installation", "client", c.Key.String(), "image", c.Image, "profile", c.Profile, "err", err)
		if ferr := s.State.Fail(c.Token, err.Error()); ferr != nil {
			s.Log.Error("boot: cannot record the failure", "client", c.Key.String(), "err", ferr)
		}
		script(w, localScript("the installation could not be started; see the Kyro server."))
		return
	}
	s.Log.Info("boot: installation started", "client", c.Key.String(), "image", c.Image, "profile", c.Profile, "mac", id.MAC, "addr", addr.String())
	if s.OnInstall != nil && addr.IsValid() {
		s.OnInstall(addr)
	}
	script(w, body)
}

// prepare checks that everything an installation needs is there and
// returns the boot script for it.
func (s *Service) prepare(c state.Client) (string, error) {
	if s.Config.SMB.Mode != "embedded" {
		return "", errors.New("the install share is switched off (smb.mode)")
	}
	if _, err := s.Images.Image(c.Image); err != nil {
		return "", fmt.Errorf("image %s: %w", c.Image, err)
	}
	if _, err := s.answerFile(c); err != nil {
		return "", err
	}
	if _, err := s.startScript(c); err != nil {
		return "", err
	}
	return installScript(s.Config.HTTPBase(), c.Token, c.Image, c.Profile)
}

// CallbackURL returns the address at which the installed system of one
// installation reports back.
func (s *Service) CallbackURL(token string) string {
	return s.Config.HTTPBase() + "/api/v1/callback/" + token
}

// answerFile returns the answer file of a client's profile with the tokens
// replaced by what the client said about itself when it booted.
func (s *Service) answerFile(c state.Client) ([]byte, error) {
	data, _, err := s.Profiles.AnswerFile(c.Profile)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", c.Profile, err)
	}
	out, err := profiles.Render(data, profiles.Values{
		MAC:         c.Seen.MAC,
		UUID:        c.Seen.UUID,
		Serial:      c.Seen.Serial,
		CallbackURL: s.CallbackURL(c.Token),
	})
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", c.Profile, err)
	}
	return out, nil
}

func (s *Service) startScript(c state.Client) ([]byte, error) {
	o, err := s.Images.Image(c.Image)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", c.Image, err)
	}
	return startScript(startParams{
		Address:  s.Config.Address,
		Port:     s.Config.SMB.Port,
		Username: s.Config.SMB.Username,
		Password: s.Config.SMB.Password,
		Image:    c.Image,
		Install:  o.Image.InstallImage,
		Extras:   o.Image.Extras,
	})
}

// handleInstallFile serves the files of one installation. They exist only
// under the token of that installation and only while it runs.
func (s *Service) handleInstallFile(w http.ResponseWriter, r *http.Request) {
	c, err := s.State.Installation(r.PathValue("token"))
	if err != nil {
		if !errors.Is(err, state.ErrNoToken) {
			s.Log.Error("boot: cannot read the client state", "err", err)
		}
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body []byte
	switch r.PathValue("file") {
	case "winpeshl.ini":
		body = []byte(winpeshl)
	case "kyro-start.cmd":
		body, err = s.startScript(c)
	case "autounattend.xml":
		body, err = s.answerFile(c)
		if err == nil {
			s.Log.Info("boot: answer file sent", "client", c.Key.String(), "profile", c.Profile, "addr", remoteAddr(r).String())
		}
	case "boot.wim":
		s.serveBootWIM(w, r, c)
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.Log.Error("boot: cannot build a file of the installation", "client", c.Key.String(), "file", r.PathValue("file"), "err", err)
		http.Error(w, "the file could not be built; see the Kyro server", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

func (s *Service) serveBootWIM(w http.ResponseWriter, r *http.Request, c state.Client) {
	o, err := s.Images.Image(c.Image)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := o.FS.Open(o.Image.BootWIM)
	if err != nil {
		s.Log.Error("boot: cannot open boot.wim", "image", c.Image, "err", err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	info, err := fs.Stat(o.FS, o.Image.BootWIM)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", info.ModTime(), rs)
}

// handleCallback is where the installed system says that it is done.
//
// The token in the path is the only thing that ties the request to a
// client. Whoever knows it can say "done" for that client, and it travels
// over plain HTTP. This marks progress; it authenticates nothing.
func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	addr := remoteAddr(r)
	if !s.callbacks.allow(addr) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	// A body is neither needed nor read beyond a small limit.
	io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 1024))

	c, changed, err := s.State.Provisioned(r.PathValue("token"))
	switch {
	case errors.Is(err, state.ErrNoToken):
		http.NotFound(w, r)
		return
	case err != nil:
		s.Log.Error("callback: cannot read or write the client state", "err", err)
		http.Error(w, "the Kyro server has a problem with its client records", http.StatusInternalServerError)
		return
	}
	if changed {
		s.Log.Info("callback: client provisioned", "client", c.Key.String(), "image", c.Image, "profile", c.Profile, "addr", addr.String())
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

// Expire marks installations as failed that have run for longer than the
// configured time. The service calls it once a minute.
func (s *Service) Expire() {
	failed, err := s.State.Expire(time.Duration(s.Config.InstallTimeout))
	if err != nil {
		s.Log.Error("cannot check for installations that take too long", "err", err)
	}
	for _, c := range failed {
		s.Log.Warn("installation failed", "client", c.Key.String(), "reason", c.Failure)
	}
}
