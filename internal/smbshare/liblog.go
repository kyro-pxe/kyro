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
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jfjallid/golog"
)

// Levels below slog's own for the library's chatter. The library reports
// every malformed request as an error; for Kyro that is a client problem,
// not a server problem, and must not flood the log.
const (
	levelClient = slog.LevelDebug     // what the library calls an error or a notice
	levelTrace  = slog.LevelDebug - 4 // the library's info and debug output
)

// libraryLogger adapts a slog.Logger to the logger interface of the SMB
// library. Messages contain text from clients; slog's handlers quote it.
type libraryLogger struct {
	log *slog.Logger
	// panics limits how often a recovered panic is logged. Nil means
	// always.
	panics *rateLimit
}

// rateLimit lets a number of events through per interval and counts the
// rest. A client can trigger a panic report as often as it can connect, and
// each one is a stack trace at error level.
type rateLimit struct {
	max      int
	interval time.Duration

	mu         sync.Mutex
	start      time.Time
	n          int
	suppressed int
}

// allow reports whether an event at time now may pass. If events were held
// back since the last one that passed, it also returns how many.
func (r *rateLimit) allow(now time.Time) (ok bool, suppressed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.start) >= r.interval {
		r.start, r.n = now, 0
	}
	if r.n >= r.max {
		r.suppressed++
		return false, 0
	}
	r.n++
	suppressed, r.suppressed = r.suppressed, 0
	return true, suppressed
}

func (l libraryLogger) emit(level slog.Level, msg string) {
	if !l.log.Enabled(context.Background(), level) {
		return
	}
	l.log.Log(context.Background(), level, strings.TrimSpace(msg))
}

// libraryError logs what the library reports as an error.
//
// The library recovers from a panic in the code that serves a connection,
// closes that connection and reports it here. That is a bug in the server,
// not noise from a client, so it is logged as an error. The library calls
// this from its deferred recovery, where the stack of the panic is still
// there; it is logged too, because without it the bug cannot be found.
func (l libraryLogger) libraryError(msg string) {
	if !strings.Contains(msg, "panic") {
		l.emit(levelClient, msg)
		return
	}
	stack := string(debug.Stack())
	if l.panics != nil {
		ok, suppressed := l.panics.allow(time.Now())
		if !ok {
			return
		}
		if suppressed > 0 {
			l.log.Error("more panics were recovered and not logged", "count", suppressed)
		}
	}
	l.log.Error(strings.TrimSpace(msg), "stack", stack)
}

func (l libraryLogger) Errorf(format string, v ...any) { l.libraryError(fmt.Sprintf(format, v...)) }
func (l libraryLogger) Errorln(v ...any)               { l.libraryError(fmt.Sprintln(v...)) }

func (l libraryLogger) Noticef(format string, v ...any) {
	l.emit(levelClient, fmt.Sprintf(format, v...))
}
func (l libraryLogger) Noticeln(v ...any) { l.emit(levelClient, fmt.Sprintln(v...)) }
func (l libraryLogger) Infof(format string, v ...any) {
	l.emit(levelTrace, fmt.Sprintf(format, v...))
}
func (l libraryLogger) Infoln(v ...any) { l.emit(levelTrace, fmt.Sprintln(v...)) }
func (l libraryLogger) Debugf(format string, v ...any) {
	l.emit(levelTrace, fmt.Sprintf(format, v...))
}
func (l libraryLogger) Debugln(v ...any) { l.emit(levelTrace, fmt.Sprintln(v...)) }

// The library also logs through loggers of its own, which write to standard
// output and standard error. captureLibraryLog points all of them at the
// logger of the most recently created server instead. They are global to
// the process, so there is one destination for all servers.
var (
	libraryLogDest atomic.Pointer[slog.Logger]
	libraryLogOnce sync.Once
)

func captureLibraryLog(log *slog.Logger) {
	libraryLogDest.Store(log)
	libraryLogOnce.Do(func() {
		w := libraryLogWriter{}
		for _, name := range golog.Names() {
			golog.Get(name).SetFlags(0).SetOutput(w).SetErrOutput(w)
		}
	})
}

type libraryLogWriter struct{}

var _ io.Writer = libraryLogWriter{}

func (libraryLogWriter) Write(p []byte) (int, error) {
	if log := libraryLogDest.Load(); log != nil {
		libraryLogger{log: log}.emit(levelClient, string(p))
	}
	return len(p), nil
}
