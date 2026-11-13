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

package smbshare

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jfjallid/go-smb/ntlmssp"
	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/smb/server"
)

// Defaults for the limits in Config.
const (
	DefaultMaxConnections = 256
	DefaultMaxOpenHandles = 4096
	DefaultIdleTimeout    = 30 * time.Minute
)

// readOnlyAccess is the access mask announced for a share: read and
// execute, nothing that writes.
const readOnlyAccess uint32 = 0x001200a9

// ErrServerClosed is returned by Serve after Close or Shutdown.
var ErrServerClosed = errors.New("smbshare: server closed")

var (
	// A share name becomes part of a UNC path in a batch file.
	shareNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	// The account name and password are written into a batch file, on a
	// "net use" command line. The character set leaves nothing to escape.
	credentialRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Config describes a server.
type Config struct {
	// Shares maps share names to the trees they serve. Files in a tree
	// must implement io.ReaderAt; the trees of os.Root and os.DirFS do.
	Shares map[string]fs.FS

	// Username and Password are the one account that can log in. They are
	// not a secret, see the package documentation.
	Username string
	Password string

	// Logger receives the log output. The default discards it.
	Logger *slog.Logger

	// MaxConnections caps the connections served at the same time.
	// Zero means DefaultMaxConnections.
	MaxConnections int

	// MaxOpenHandles caps the open files and directories of all clients
	// together. Zero means DefaultMaxOpenHandles.
	MaxOpenHandles int

	// IdleTimeout is how long a connection may send nothing before it is
	// closed. Zero means DefaultIdleTimeout. A client that disappears is
	// noticed much sooner if the listener comes from Listen.
	IdleTimeout time.Duration
}

// Server is a read-only SMB server.
type Server struct {
	srv *server.Server
	log *slog.Logger

	mu    sync.Mutex
	conns map[*trackedConn]struct{}
}

// New checks cfg and returns a server that is not listening yet.
func New(cfg Config) (*Server, error) {
	if len(cfg.Shares) == 0 {
		return nil, errors.New("smbshare: no shares configured")
	}
	if !credentialRE.MatchString(cfg.Username) {
		return nil, fmt.Errorf("smbshare: invalid user name %q: use 1 to 64 letters, digits, dots, underscores and hyphens", cfg.Username)
	}
	if !credentialRE.MatchString(cfg.Password) {
		return nil, errors.New("smbshare: invalid password: use 1 to 64 letters, digits, dots, underscores and hyphens")
	}
	if cfg.MaxConnections < 0 || cfg.MaxOpenHandles < 0 || cfg.IdleTimeout < 0 {
		return nil, errors.New("smbshare: limits must not be negative")
	}
	if cfg.MaxConnections == 0 {
		cfg.MaxConnections = DefaultMaxConnections
	}
	if cfg.MaxOpenHandles == 0 {
		cfg.MaxOpenHandles = DefaultMaxOpenHandles
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	handles := newHandleTable(cfg.MaxOpenHandles)
	shares := make(map[string]server.Share, len(cfg.Shares))
	for name, fsys := range cfg.Shares {
		if !shareNameRE.MatchString(name) {
			return nil, fmt.Errorf("smbshare: invalid share name %q: use 1 to 32 letters, digits, underscores and hyphens", name)
		}
		if fsys == nil {
			return nil, fmt.Errorf("smbshare: share %q has no file system", name)
		}
		key := strings.ToLower(name)
		if _, dup := shares[key]; dup {
			return nil, fmt.Errorf("smbshare: share name %q is used twice (names ignore case)", name)
		}
		shares[key] = server.Share{
			Name:          name,
			Type:          smb.ShareTypeDisk,
			VFS:           &shareFS{fsys: fsys, handles: handles, log: log.With("share", name)},
			MaximalAccess: readOnlyAccess,
		}
	}

	captureLibraryLog(log)
	s := &Server{log: log, conns: make(map[*trackedConn]struct{})}
	s.srv = &server.Server{Config: &server.ServerConfig{
		NetBIOSName: "KYRO",

		// One dialect. SMB 3.1.1 protects the negotiation itself, and every
		// Windows version that Kyro installs speaks it.
		MinDialect: smb.DialectSmb_3_1_1,
		MaxDialect: smb.DialectSmb_3_1_1,

		SigningRequired: true,
		Authenticator: &server.MapAuthenticator{
			Accounts: map[string]*server.Account{
				strings.ToLower(cfg.Username): {NTHash: ntlmssp.Ntowfv1(cfg.Password)},
			},
		},
		// Everything optional stays off: no anonymous or guest sessions, no
		// encryption (the content is public), no compression, no durable
		// handles and no named pipes.
		AllowAnonymous:      false,
		AllowGuest:          false,
		EncryptionSupported: false,
		Compression:         false,
		DurableHandles:      false,
		PipeOpener:          nil,

		// The library closes the handles of a session when its connection
		// ends, but not when the client logs off. Without this a client
		// could fill the handle table for good by logging in, opening
		// files and logging off, over and over.
		OnLogoff: func(_ *server.Conn, sess *server.Session) {
			handles.closeWhere(func(s *server.Session) bool { return s == sess })
		},
		// A safety net for any other way a handle could outlive its
		// connection.
		OnDisconnect: func(c *server.Conn) {
			n := handles.closeWhere(func(s *server.Session) bool { return s != nil && s.Conn == c })
			if n > 0 {
				log.Warn("closed handles that the SMB library left open", "count", n)
			}
		},

		Shares:         shares,
		MaxConnections: cfg.MaxConnections,
		IdleTimeout:    cfg.IdleTimeout,
		Logger: libraryLogger{
			log:    log,
			panics: &rateLimit{max: 5, interval: time.Minute},
		},
	}}
	return s, nil
}

// Listen opens a TCP listener for a server. Connections accepted from it
// are closed about half a minute after their client stops responding.
//
// That matters because a client that is reset in the middle of an
// installation never closes its connection. Windows PE then waits minutes
// for the share on its next boot.
func Listen(addr string) (net.Listener, error) {
	lc := net.ListenConfig{
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     10 * time.Second,
			Interval: 5 * time.Second,
			Count:    4,
		},
		Control: setUserTimeout,
	}
	return lc.Listen(context.Background(), "tcp", addr)
}

// peerTimeout is how long sent data may stay unacknowledged before the
// connection is closed.
const peerTimeout = 30 * time.Second

// ListenAndServe listens on addr and serves until Close or Shutdown.
func (s *Server) ListenAndServe(addr string) error {
	l, err := Listen(addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

// Serve accepts connections from l until Close or Shutdown. It always
// returns an error; after Close or Shutdown that is ErrServerClosed.
func (s *Server) Serve(l net.Listener) error {
	// The library leaves l open if the server was closed before Serve.
	defer l.Close()
	err := s.srv.Serve(&trackingListener{Listener: l, server: s})
	if errors.Is(err, server.ErrServerClosed) {
		return ErrServerClosed
	}
	return err
}

// Close stops the server and closes every connection.
func (s *Server) Close() error {
	return s.srv.Close()
}

// Shutdown stops accepting connections and waits until the existing ones
// are gone or ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// DropClient closes every connection from the given address and returns
// how many there were. Kyro calls it when a client starts to boot: whatever
// that client had open belongs to an installation that no longer runs.
func (s *Server) DropClient(addr netip.Addr) int {
	addr = addr.Unmap()
	s.mu.Lock()
	var drop []*trackedConn
	for c := range s.conns {
		if c.remote == addr {
			drop = append(drop, c)
		}
	}
	s.mu.Unlock()
	for _, c := range drop {
		c.Close()
	}
	return len(drop)
}

// trackingListener records the accepted connections so that DropClient can
// find them.
type trackingListener struct {
	net.Listener
	server *Server
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, server: l.server}
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		tc.remote = ap.Addr().Unmap()
	}
	l.server.mu.Lock()
	l.server.conns[tc] = struct{}{}
	l.server.mu.Unlock()
	return tc, nil
}

type trackedConn struct {
	net.Conn
	server *Server
	remote netip.Addr
	once   sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.server.mu.Lock()
		delete(c.server.conns, c)
		c.server.mu.Unlock()
	})
	return c.Conn.Close()
}
