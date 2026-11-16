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
	// Expired is a client that was armed and did not boot from the network
	// before its arming ran out. It is not installed.
	Expired State = "expired"
)

// Stage says how far an installation has come. It is what Kyro saw or was
// told, not proof: the reports from Windows PE can be forged by whoever
// knows the token of the installation, like the callback.
type Stage string

// The stages, in the order in which an installation passes them. Not every
// installation reports every one.
const (
	// StageInstallerSent: the client was given the boot script that loads
	// Windows PE. Nothing has been heard from Windows PE yet.
	StageInstallerSent Stage = "installer-sent"
	// StageShareConnected: Windows PE runs, has a network, and has
	// connected the install share.
	StageShareConnected Stage = "share-connected"
	// StageSetupStarted: the start script is about to start Windows Setup.
	StageSetupStarted Stage = "setup-started"
	// StageSetupFinished: Windows Setup ended its part in Windows PE
	// without an error.
	StageSetupFinished Stage = "setup-finished"
	// StageRestarted: the machine has booted from the network again since
	// it was given the installer, as Setup makes it do.
	StageRestarted Stage = "restarted"
)

// rank orders the stages. A stage is only ever replaced by a later one.
func (s Stage) rank() int {
	switch s {
	case StageInstallerSent:
		return 1
	case StageShareConnected:
		return 2
	case StageSetupStarted:
		return 3
	case StageSetupFinished:
		return 4
	case StageRestarted:
		return 5
	}
	return 0
}

// How long an arming may last. A client that is armed and forgotten must
// not be wiped when it happens to boot from the network weeks later, so
// every arming ends by itself.
const (
	DefaultArmLifetime = 24 * time.Hour
	MinArmLifetime     = time.Minute
	MaxArmLifetime     = 30 * 24 * time.Hour
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

	ArmedAt *time.Time `json:"armed_at,omitempty"`
	// ArmedUntil is when the arming runs out. From then on the client is
	// not installed any more.
	ArmedUntil *time.Time `json:"armed_until,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`

	// Stage is how far the installation has come, StageAt since when, and
	// Restarts how often the machine has booted from the network since it
	// was given the installer. They stay after the installation has ended.
	Stage    Stage      `json:"stage,omitempty"`
	StageAt  *time.Time `json:"stage_at,omitempty"`
	Restarts int        `json:"restarts,omitempty"`

	// Failure says why the client is in state failed or expired.
	Failure string `json:"failure,omitempty"`
}

// advance moves the client to a later stage and says whether it did.
func (c *Client) advance(to Stage, now *time.Time) bool {
	if to.rank() <= c.Stage.rank() {
		return false
	}
	c.Stage, c.StageAt = to, now
	return true
}

// stalled describes, for an installation that did not finish, where it
// stopped and what usually causes that. It is advice, not a finding.
func (c *Client) stalled() string {
	switch c.Stage {
	case StageShareConnected, StageSetupStarted:
		return "Windows PE connected to the install share and started Windows Setup, and the machine never restarted. Usual causes: Setup found no disk (a missing storage driver, see 'kyro driver'), or it rejected the answer file. Setup shows the reason on the client's screen"
	case StageSetupFinished, StageRestarted:
		return fmt.Sprintf("Windows Setup finished in Windows PE and the machine restarted %d times, but the installed system did not call back. Usual causes: the answer file has no callback command, the installed Windows has no driver for the network card, or Setup is waiting for input on the client's screen", c.Restarts)
	}
	return "the client was given the installer, and Windows PE never connected to the install share. Usual causes: Windows PE has no driver for the network card (see 'kyro driver'), or it got no address. The client's screen shows where it stopped"
}

// armedUntil returns when the arming of an armed client runs out. A record
// that does not say, as written by a version without this rule, is treated
// as armed with the default lifetime. A record without any time counts as
// run out: when in doubt, no installation.
func (c *Client) armedUntil() time.Time {
	switch {
	case c.ArmedUntil != nil:
		return *c.ArmedUntil
	case c.ArmedAt != nil:
		return c.ArmedAt.Add(DefaultArmLifetime)
	}
	return time.Time{}
}

// expire turns an armed client whose arming has run out into an expired
// one and says whether it did. The token goes: nothing opens with it.
func (c *Client) expire(now *time.Time) bool {
	if c.State != Armed {
		return false
	}
	until := c.armedUntil()
	if now.Before(until) {
		return false
	}
	c.State, c.Token, c.FinishedAt = Expired, "", now
	c.Failure = "the arming ran out before the client booted from the network"
	if !until.IsZero() {
		c.Failure = fmt.Sprintf("was armed until %s UTC and did not boot from the network before that", until.UTC().Format("2006-01-02 15:04"))
	}
	return true
}

// Errors that callers tell apart.
var (
	ErrInstalling = errors.New("the client is being installed right now")
	ErrNotArmed   = errors.New("the client is not armed")
	ErrNoToken    = errors.New("no installation with this token")
	ErrLifetime   = fmt.Errorf("an arming lasts between %s and %s", MinArmLifetime, MaxArmLifetime)
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

// Arming is what a client is armed for.
type Arming struct {
	Image   string
	Profile string
	// Lifetime is how long the arming lasts. If the client has not booted
	// from the network by then, it is not installed.
	Lifetime time.Duration
}

// Arm marks a client for one installation. Arming an armed client replaces
// what it was armed for. A client that is being installed is only armed
// again with force.
func (s *Store) Arm(k Key, a Arming, force bool) (Client, error) {
	if a.Lifetime < MinArmLifetime || a.Lifetime > MaxArmLifetime {
		return Client{}, ErrLifetime
	}
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
	c.State, c.Image, c.Profile, c.Token = Armed, a.Image, a.Profile, token
	c.ArmedAt, c.StartedAt, c.FinishedAt, c.Failure = s.now(), nil, nil, ""
	c.Stage, c.StageAt, c.Restarts = "", nil, 0
	until := c.ArmedAt.Add(a.Lifetime)
	c.ArmedUntil = &until
	return *c, s.write(c)
}

// Disarm takes back an Arm that the client has not used yet. A client that
// was seen before goes back to unknown; one that was never seen is
// forgotten. An arming that has run out cannot be taken back any more: the
// record then says expired, and stays so.
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
	if c != nil && c.expire(s.now()) {
		if err := s.write(c); err != nil {
			return *c, err
		}
	}
	if c == nil || c.State != Armed {
		if c == nil {
			c = &Client{Key: k, State: Unknown}
		}
		return *c, ErrNotArmed
	}
	c.State, c.Image, c.Profile, c.Token, c.ArmedAt, c.ArmedUntil = Unknown, "", "", "", nil, nil
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

	// An arming that has run out installs nothing. This is decided here,
	// under the lock and with the clock of this moment, and not left to
	// the sweep that runs once a minute.
	for _, c := range found {
		if c.expire(s.now()) {
			if err := s.write(c); err != nil {
				return Decision{}, err
			}
		}
	}

	for _, c := range found {
		if c.State != Armed {
			continue
		}
		// The record is written before the answer goes out. If that
		// fails, the client gets nothing: better no installation than
		// one that could repeat.
		c.State, c.StartedAt = Installing, s.now()
		c.Stage, c.StageAt, c.Restarts = StageInstallerSent, c.StartedAt, 0
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
		// Prefer the record of a running installation: its restarts are
		// counted there.
		for _, f := range found {
			if f.State == Installing {
				c = f
				break
			}
		}
		seen(c)
		if c.State == Installing {
			// Setup restarts the machine; with the network first in the
			// boot order, each restart comes by here.
			c.Restarts++
			c.advance(StageRestarted, s.now())
		}
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

// Events that Windows PE reports, see Report.
const (
	EventShareConnected = "share-connected"
	EventSetupStarted   = "setup-started"
	EventSetupFinished  = "setup-finished"
	EventSetupFailed    = "setup-failed"
)

// Report records what the start script in Windows PE says about the
// installation under a token: that it reached a stage, or that Windows
// Setup ended with an error, which ends the installation as failed. detail
// is the exit code of Setup in that case. changed says whether the record
// changed; reports that repeat or come late change nothing.
//
// Like the callback, a report proves nothing: whoever knows the token can
// send it.
func (s *Store) Report(token, event, detail string) (c Client, changed bool, err error) {
	var stage Stage
	switch event {
	case EventShareConnected:
		stage = StageShareConnected
	case EventSetupStarted:
		stage = StageSetupStarted
	case EventSetupFinished:
		stage = StageSetupFinished
	case EventSetupFailed:
	default:
		return Client{}, false, fmt.Errorf("unknown event %q", event)
	}
	unlock, err := s.lock()
	if err != nil {
		return Client{}, false, err
	}
	defer unlock()

	rec, err := s.findToken(token)
	if err != nil {
		return Client{}, false, err
	}
	if rec == nil || rec.State != Installing {
		return Client{}, false, ErrNoToken
	}
	if event == EventSetupFailed {
		rec.State, rec.FinishedAt = Failed, s.now()
		rec.Failure = "Windows Setup ended with an error in Windows PE"
		if detail != "" {
			rec.Failure += " (exit code " + detail + ")"
		}
		rec.Failure += ". Its logs are on the client, in X:\\Windows\\Panther, and its screen shows the reason"
		return *rec, true, s.write(rec)
	}
	if !rec.advance(stage, s.now()) {
		return *rec, false, nil
	}
	return *rec, true, s.write(rec)
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
// than timeout, and every armed client as expired whose arming has run out.
// It returns the clients it changed.
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
		switch {
		case c.expire(s.now()):
		case c.State == Installing && c.StartedAt != nil && s.Now().Sub(*c.StartedAt) > timeout:
			c.State, c.FinishedAt = Failed, s.now()
			c.Failure = fmt.Sprintf("the installed system did not report back within %s: %s", timeout, c.stalled())
		default:
			continue
		}
		if err := s.write(c); err != nil {
			return out, err
		}
		out = append(out, *c)
	}
	return out, nil
}
