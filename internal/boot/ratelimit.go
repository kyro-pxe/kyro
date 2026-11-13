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

package boot

import (
	"net/netip"
	"sync"
	"time"
)

// rateLimit allows each address a number of requests in a row and then a
// steady trickle. It is for the endpoints that write to disk because of a
// request from the network.
type rateLimit struct {
	burst    float64
	perSec   float64
	maxAddrs int

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
	shared  bucket // for everyone once the table is full
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimit(burst int, perSec float64) *rateLimit {
	return &rateLimit{
		burst:    float64(burst),
		perSec:   perSec,
		maxAddrs: 4096,
		buckets:  make(map[netip.Addr]*bucket),
		now:      time.Now,
	}
}

// allow reports whether a request from addr may go ahead.
func (r *rateLimit) allow(addr netip.Addr) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	b := r.buckets[addr]
	if b == nil {
		if len(r.buckets) >= r.maxAddrs {
			// Forget the addresses that have a full bucket again. If the
			// table is still full, someone is inventing addresses, and
			// everyone new shares one bucket.
			for a, old := range r.buckets {
				if now.Sub(old.last).Seconds()*r.perSec >= r.burst {
					delete(r.buckets, a)
				}
			}
		}
		if len(r.buckets) >= r.maxAddrs {
			b = &r.shared
			if b.last.IsZero() {
				b.tokens, b.last = r.burst, now
			}
		} else {
			b = &bucket{tokens: r.burst, last: now}
			r.buckets[addr] = b
		}
	}
	b.tokens = min(r.burst, b.tokens+now.Sub(b.last).Seconds()*r.perSec)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
