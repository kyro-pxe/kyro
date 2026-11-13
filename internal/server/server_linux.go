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

//go:build linux

// Package server runs all of Kyro's services together.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/kyro-pxe/kyro/internal/boot"
	"github.com/kyro-pxe/kyro/internal/bootfiles"
	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/dhcp"
	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/smbshare"
	"github.com/kyro-pxe/kyro/internal/state"
	"github.com/kyro-pxe/kyro/internal/tftp"
)

// Options changes how Run behaves. The zero value is what the service
// uses.
type Options struct {
	// BootFiles replaces the pinned iPXE and wimboot files. Tests use
	// stand-ins.
	BootFiles []bootfiles.File
	// SkipInterfaceCheck leaves out the check that the configured address
	// is on the configured interface.
	SkipInterfaceCheck bool
	// Ready, if set, is called once every service listens.
	Ready func()
}

// checkInterface verifies that the interface exists, is up, and carries
// the configured address.
func checkInterface(cfg config.Config) error {
	ifi, err := net.InterfaceByName(cfg.Interface)
	if err != nil {
		return fmt.Errorf("interface %s: %w", cfg.Interface, err)
	}
	if ifi.Flags&net.FlagUp == 0 {
		return fmt.Errorf("interface %s is down", cfg.Interface)
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return fmt.Errorf("interface %s: %w", cfg.Interface, err)
	}
	var have []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if n.IP.String() == cfg.Address {
				return nil
			}
			if n.IP.To4() != nil {
				have = append(have, n.IP.String())
			}
		}
	}
	return fmt.Errorf("interface %s does not have the address %s (it has %v); correct 'address' or 'interface' in the configuration", cfg.Interface, cfg.Address, have)
}

// Run starts every configured service and returns when ctx is done or one
// of them fails. Nothing is left running when it returns.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger, opts Options) error {
	// The configuration was checked when it was loaded; this only turns
	// the address into its parsed form.
	addr, err := netip.ParseAddr(cfg.Address)
	if err != nil {
		return fmt.Errorf("address %q: %w", cfg.Address, err)
	}
	if !opts.SkipInterfaceCheck {
		if err := checkInterface(cfg); err != nil {
			return err
		}
	}
	files := opts.BootFiles
	if files == nil {
		files = bootfiles.Files
	}
	loaded, err := bootfiles.Load(cfg.BootDir(), files)
	if err != nil {
		return err
	}
	st, err := state.Open(cfg.StateDir())
	if err != nil {
		return fmt.Errorf("client state: %w", err)
	}
	// Reading the records now finds a damaged one before a client does.
	if _, err := st.List(); err != nil {
		return fmt.Errorf("client state: %w", err)
	}
	tree := images.Open(cfg.ImagesDir()).Tree()
	defer tree.Close()

	svc := &boot.Service{
		Config:   cfg,
		State:    st,
		Images:   tree,
		Profiles: profiles.Open(cfg.ProfilesDir()),
		Wimboot:  loaded["wimboot"],
		Log:      log,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	// start runs one service. Its first error stops all of them.
	start := func(name string, run func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(); err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("%s: %w", name, err)
				cancel()
			}
		}()
	}
	// Listening happens here, before anything is started, so that a port
	// that is taken is reported at once and by name.
	listen := func(what string, ap netip.AddrPort) (net.Listener, error) {
		l, err := net.Listen("tcp4", ap.String())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		return l, nil
	}

	// The install share.
	if cfg.SMB.Mode == "embedded" {
		smb, err := smbshare.New(smbshare.Config{
			Shares:   map[string]fs.FS{boot.ShareName: tree},
			Username: cfg.SMB.Username,
			Password: cfg.SMB.Password,
			Logger:   log.With("service", "smb"),
		})
		if err != nil {
			return err
		}
		l, err := smbshare.Listen(netip.AddrPortFrom(addr, uint16(cfg.SMB.Port)).String())
		if err != nil {
			return fmt.Errorf("install share: %w", err)
		}
		// A client that starts an installation has nothing open that still
		// matters. What it left behind on its last boot would only make
		// Windows PE wait.
		svc.OnInstall = func(a netip.Addr) { smb.DropClient(a) }
		start("install share", func() error {
			if err := smb.Serve(l); !errors.Is(err, smbshare.ErrServerClosed) {
				return err
			}
			return nil
		})
		context.AfterFunc(ctx, func() { smb.Close() })
		log.Info("install share listening", "addr", l.Addr().String(), "share", boot.ShareName)
	}

	// HTTP for booting clients.
	bootListener, err := listen("boot HTTP", netip.AddrPortFrom(addr, uint16(cfg.HTTP.Port)))
	if err != nil {
		cancel()
		wg.Wait()
		return err
	}
	bootServer := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.With("service", "http").Handler(), slog.LevelDebug),
	}
	serveHTTP := func(name string, srv *http.Server, l net.Listener) {
		start(name, func() error {
			if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		context.AfterFunc(ctx, func() { srv.Close() })
	}
	serveHTTP("boot HTTP", bootServer, bootListener)
	log.Info("boot HTTP listening", "addr", bootListener.Addr().String())

	// The status page, on its own address.
	if cfg.Status.Listen != "" {
		ap, _ := netip.ParseAddrPort(cfg.Status.Listen)
		l, err := listen("status page", ap)
		if err != nil {
			cancel()
			wg.Wait()
			return err
		}
		serveHTTP("status page", &http.Server{
			Handler:           svc.StatusHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			MaxHeaderBytes:    16 << 10,
		}, l)
		log.Info("status page listening", "addr", l.Addr().String())
	}

	if cfg.TFTPEnabled() {
		t := &tftp.Server{
			Addr: netip.AddrPortFrom(addr, 69),
			Lookup: func(name string) ([]byte, bool) {
				// wimboot goes over HTTP; only the iPXE binaries are for
				// firmware.
				if name == "wimboot" {
					return nil, false
				}
				data, ok := loaded[name]
				return data, ok
			},
			Log: log,
		}
		start("TFTP", func() error { return t.Serve(ctx) })
		log.Info("TFTP listening", "addr", t.Addr.String())
	}

	switch cfg.DHCP.Mode {
	case "proxy":
		d := &dhcp.Server{
			Interface: cfg.Interface,
			Addr:      addr,
			Responder: &dhcp.Proxy{
				Addr: addr,
				Files: dhcp.BootFiles{
					BIOS:   "undionly.kpxe",
					UEFI64: "ipxe.efi",
					IPXE:   cfg.HTTPBase() + "/boot.ipxe",
				},
				Log: log,
			},
			Log: log,
		}
		start("DHCP", func() error { return d.Serve(ctx) })
		log.Info("proxyDHCP listening", "interface", cfg.Interface, "ports", "67 and 4011")
	case "full":
		cancel()
		wg.Wait()
		return errors.New("dhcp.mode full is not implemented in this version; use proxy with an existing DHCP server, or off")
	}

	// Installations that never report back are marked as failed.
	start("timeouts", func() error {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			svc.Expire()
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	})

	if opts.Ready != nil {
		opts.Ready()
	}
	var first error
	select {
	case <-ctx.Done():
	case first = <-errs:
	}
	cancel()
	wg.Wait()
	return first
}
