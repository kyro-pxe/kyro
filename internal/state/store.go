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

// Package state keeps what Kyro knows about each client.
//
// A client is in one of five states:
//
//	unknown      seen on the network, never armed (or disarmed again)
//	armed        will be installed on its next network boot, once
//	installing   was given the installer; waiting for it to report back
//	provisioned  the installed system reported back
//	failed       did not report back in time
//
// The rule that everything else follows from: a client gets the installer
// only when it is armed, and getting it ends being armed. Whatever happens
// afterwards, a client that boots from the network again is sent to its
// local disk until an administrator arms it again.
//
// Records are JSON files, one per client, in one directory. See ADR 0004.
// The CLI and the running service both use this package on the same
// directory; a lock file keeps their changes apart.
package state

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// State is the state of a client.
type State string

// The states of a client.
const (
	Unknown     State = "unknown"
	Armed       State = "armed"
	Installing  State = "installing"
	Provisioned State = "provisioned"
	Failed      State = "failed"
)

// maxUnknown limits how many clients are remembered that were never armed.
// Their records are written because of a request from the network, and
// nothing stops a host from inventing addresses.
const maxUnknown = 1024

// Client is the record of one client.
type Client struct {
	Key   Key   `json:"key"`
	State State `json:"state"`

	// Image and Profile are what the client is or was armed for.
	Image   string `json:"image,omitempty"`
	Profile string `json:"profile,omitempty"`
	// Token names this one installation in URLs. It is random, it is made
	// when the client is armed, and it stops working when the installation
	// ends.
	Token string `json:"token,omitempty"`

	// Seen is what the machine said about itself the last time it booted.
	Seen     Identity `json:"seen"`
	LastAddr string   `json:"last_addr,omitempty"`
	Boots    int      `json:"boots,omitempty"`

	ArmedAt    *time.Time `json:"armed_at,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`

	// Failure says why the client is in state failed.
	Failure string `json:"failure,omitempty"`
}

// Errors that callers tell apart.
var (
	ErrInstalling = errors.New("the client is being installed right now")
	ErrNotArmed   = errors.New("the client is not armed")
	ErrNoToken    = errors.New("no installation with this token")
)

// Store is a directory of client records.
type Store struct {
	dir string
	// Now returns the current time. Tests replace it.
	Now func() time.Time
}

// Open returns the store in dir and creates the directory if it is missing.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{dir: dir, Now: time.Now}, nil
}

func (s *Store) now() *time.Time {
	t := s.Now().UTC().Truncate(time.Second)
	return &t
}

// path returns where the record of a key lives. It fails for a key whose
// name would not stay inside the directory.
func (s *Store) path(k Key) (string, error) {
	name, err := k.fileName()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, name), nil
}

// read returns the record of a key, or nil if there is none.
func (s *Store) read(k Key) (*Client, error) {
	path, err := s.path(k)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Client
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Key != k {
		return nil, fmt.Errorf("%s: the record is for %s", path, c.Key)
	}
	return &c, nil
}

// write stores a record so that a reader sees the old or the new content,
// never half of one, and so that it survives a power cut once write returns.
func (s *Store) write(c *Client) error {
	path, err := s.path(c.Key)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	matchOwner(tmp, s.dir)
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func (s *Store) remove(k Key) error {
	path, err := s.path(k)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// all reads every record, sorted by key.
func (s *Store) all() ([]*Client, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Client
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed while we were listing
			}
			return nil, err
		}
		var c Client
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(s.dir, name), err)
		}
		if want, err := c.Key.fileName(); err != nil || want != name {
			return nil, fmt.Errorf("%s: the record is for %s", filepath.Join(s.dir, name), c.Key)
		}
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out, nil
}

// List returns every client, sorted by key.
func (s *Store) List() ([]Client, error) {
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	out := make([]Client, len(all))
	for i, c := range all {
		out[i] = *c
	}
	return out, nil
}

// Get returns the record of a key.
func (s *Store) Get(k Key) (Client, bool, error) {
	c, err := s.read(k)
	if err != nil || c == nil {
		return Client{}, false, err
	}
	return *c, true, nil
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Arm marks a client for one installation of the given image with the
// given profile. Arming an armed client replaces what it was armed for. A
// client that is being installed is only armed again with force.
func (s *Store) Arm(k Key, image, profile string, force bool) (Client, error) {
	unlock, err := s.lock()
	if err != nil {
		return Client{}, err
	}
	defer unlock()

	c, err := s.read(k)
	if err != nil {
		return Client{}, err
	}
	if c == nil {
		c = &Client{Key: k}
	}
	if c.State == Installing && !force {
		return *c, ErrInstalling
	}
	token, err := newToken()
	if err != nil {
		return Client{}, err
	}
	c.State, c.Image, c.Profile, c.Token = Armed, image, profile, token
	c.ArmedAt, c.StartedAt, c.FinishedAt, c.Failure = s.now(), nil, nil, ""
	return *c, s.write(c)
}

// Disarm takes back an Arm that the client has not used yet. A client that
// was seen before goes back to unknown; one that was never seen is
// forgotten.
func (s *Store) Disarm(k Key) (Client, error) {
	unlock, err := s.lock()
	if err != nil {
		return Client{}, err
	}
	defer unlock()

	c, err := s.read(k)
	if err != nil {
		return Client{}, err
	}
	if c == nil || c.State != Armed {
		if c == nil {
			c = &Client{Key: k, State: Unknown}
		}
		return *c, ErrNotArmed
	}
	c.State, c.Image, c.Profile, c.Token, c.ArmedAt = Unknown, "", "", "", nil
	if c.LastSeen == nil {
		return *c, s.remove(k)
	}
	return *c, s.write(c)
}

// Forget removes the record of a client, whatever its state.
func (s *Store) Forget(k Key) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.remove(k)
}

// Decision is what a booting client is told.
type Decision struct {
	// Install is true if the client gets the installer. It is true at most
	// once per Arm.
	Install bool
	// Client is the record that the decision was made from. Its Key is
	// empty if the client is not recorded at all.
	Client Client
}

// Boot records that a machine boots from the network and decides what it
// gets. addr is the network address the request came from.
func (s *Store) Boot(id Identity, addr string) (Decision, error) {
	unlock, err := s.lock()
	if err != nil {
		return Decision{}, err
	}
	defer unlock()

	var found []*Client
	for _, k := range id.Keys() {
		c, err := s.read(k)
		if err != nil {
			return Decision{}, err
		}
		if c != nil {
			found = append(found, c)
		}
	}
	seen := func(c *Client) {
		c.Seen, c.LastAddr, c.LastSeen = id, addr, s.now()
		c.Boots++
	}

	for _, c := range found {
		if c.State != Armed {
			continue
		}
		// The record is written before the answer goes out. If that
		// fails, the client gets nothing: better no installation than
		// one that could repeat.
		c.State, c.StartedAt = Installing, s.now()
		seen(c)
		if err := s.write(c); err != nil {
			return Decision{}, err
		}
		// The machine is known under this key now. A leftover record
		// from when it was unknown would only show it twice.
		for _, other := range found {
			if other != c && other.State == Unknown {
				s.remove(other.Key)
			}
		}
		return Decision{Install: true, Client: *c}, nil
	}

	if len(found) > 0 {
		c := found[0]
		seen(c)
		return Decision{Client: *c}, s.write(c)
	}

	// Never seen, never armed. Remember it by its MAC address, so that an
	// administrator can find it in the list and arm it.
	if id.MAC == "" {
		return Decision{}, nil
	}
	all, err := s.all()
	if err != nil {
		return Decision{}, err
	}
	unknown := 0
	for _, c := range all {
		if c.State == Unknown {
			unknown++
		}
	}
	if unknown >= maxUnknown {
		return Decision{}, nil
	}
	c := &Client{Key: Key{KindMAC, id.MAC}, State: Unknown}
	seen(c)
	return Decision{Client: *c}, s.write(c)
}

// findToken returns the record with a token, or nil.
func (s *Store) findToken(token string) (*Client, error) {
	if len(token) != 32 {
		return nil, nil
	}
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	var match *Client
	for _, c := range all {
		if subtle.ConstantTimeCompare([]byte(c.Token), []byte(token)) == 1 {
			match = c
		}
	}
	return match, nil
}

// Installation returns the client that is being installed under a token.
// Tokens of installations that have ended are not found.
func (s *Store) Installation(token string) (Client, error) {
	c, err := s.findToken(token)
	if err != nil {
		return Client{}, err
	}
	if c == nil || c.State != Installing {
		return Client{}, ErrNoToken
	}
	return *c, nil
}

// Provisioned records that the installed system reported back. Reporting
// twice is not an error. changed says whether this call changed the state.
func (s *Store) Provisioned(token string) (c Client, changed bool, err error) {
	unlock, err := s.lock()
	if err != nil {
		return Client{}, false, err
	}
	defer unlock()

	rec, err := s.findToken(token)
	if err != nil {
		return Client{}, false, err
	}
	if rec == nil {
		return Client{}, false, ErrNoToken
	}
	switch rec.State {
	case Provisioned:
		return *rec, false, nil
	case Installing:
		rec.State, rec.FinishedAt = Provisioned, s.now()
		return *rec, true, s.write(rec)
	}
	return Client{}, false, ErrNoToken
}

// Fail ends the installation under a token as failed, with the reason. It
// is for failures that Kyro notices itself, such as an image that is gone
// by the time the client asks for it.
func (s *Store) Fail(token, reason string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	c, err := s.findToken(token)
	if err != nil {
		return err
	}
	if c == nil || c.State != Installing {
		return ErrNoToken
	}
	c.State, c.FinishedAt, c.Failure = Failed, s.now(), reason
	return s.write(c)
}

// Expire marks every client as failed that has been installing for longer
// than timeout, and returns them.
func (s *Store) Expire(timeout time.Duration) ([]Client, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	all, err := s.all()
	if err != nil {
		return nil, err
	}
	var out []Client
	for _, c := range all {
		if c.State != Installing || c.StartedAt == nil || s.Now().Sub(*c.StartedAt) <= timeout {
			continue
		}
		c.State, c.FinishedAt = Failed, s.now()
		c.Failure = fmt.Sprintf("the installed system did not report back within %s", timeout)
		if err := s.write(c); err != nil {
			return out, err
		}
		out = append(out, *c)
	}
	return out, nil
}
