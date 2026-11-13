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
	"encoding/json"
	"html/template"
	"net/http"
	"time"

	"github.com/kyro-pxe/kyro/internal/state"
)

// ClientView is what the status page and the API show of a client. The
// token of an installation is left out: it opens the files of that
// installation.
type ClientView struct {
	Client     string     `json:"client"`
	State      string     `json:"state"`
	Image      string     `json:"image,omitempty"`
	Profile    string     `json:"profile,omitempty"`
	MAC        string     `json:"mac,omitempty"`
	UUID       string     `json:"uuid,omitempty"`
	Serial     string     `json:"serial,omitempty"`
	LastAddr   string     `json:"last_addr,omitempty"`
	Boots      int        `json:"boots"`
	ArmedAt    *time.Time `json:"armed_at,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	Failure    string     `json:"failure,omitempty"`
}

// View turns a client record into what may be shown.
func View(c state.Client) ClientView {
	v := ClientView{
		Client: c.Key.String(), State: string(c.State), Image: c.Image, Profile: c.Profile,
		UUID: c.Seen.UUID, Serial: c.Seen.Serial, LastAddr: c.LastAddr, Boots: c.Boots,
		ArmedAt: c.ArmedAt, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt, LastSeen: c.LastSeen,
		Failure: c.Failure,
	}
	if c.Seen.MAC != "" {
		v.MAC = state.Key{Kind: state.KindMAC, Value: c.Seen.MAC}.Display()
	}
	return v
}

var statusPage = template.Must(template.New("status").Funcs(template.FuncMap{
	"when": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format("2006-01-02 15:04:05")
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="10">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Kyro</title>
<style>
body { font: 15px/1.4 system-ui, sans-serif; margin: 2rem; color: #1a1a1a; background: #fff; }
h1 { font-size: 1.3rem; }
table { border-collapse: collapse; }
th, td { text-align: left; padding: .35rem .9rem .35rem 0; border-bottom: 1px solid #ddd; vertical-align: top; }
th { font-weight: 600; }
.s { font-weight: 600; }
.armed { color: #8a5a00; } .installing { color: #0b57a4; } .provisioned { color: #1a7f37; } .failed { color: #b3261e; } .unknown { color: #666; }
p.note { color: #555; max-width: 46rem; }
@media (prefers-color-scheme: dark) { body { color: #e6e6e6; background: #161616; } th, td { border-color: #333; } p.note { color: #aaa; }
.armed { color: #e0a84a; } .installing { color: #6aa9f0; } .provisioned { color: #5cc77c; } .failed { color: #f08a84; } .unknown { color: #999; } }
</style>
</head>
<body>
<h1>Kyro clients</h1>
{{if .Clients}}
<table>
<tr><th>Client</th><th>State</th><th>Image</th><th>Profile</th><th>MAC</th><th>Serial</th><th>Last seen (UTC)</th><th>Note</th></tr>
{{range .Clients}}<tr>
<td>{{.Client}}</td><td class="s {{.State}}">{{.State}}</td><td>{{.Image}}</td><td>{{.Profile}}</td>
<td>{{.MAC}}</td><td>{{.Serial}}</td><td>{{when .LastSeen}}</td><td>{{.Failure}}</td>
</tr>
{{end}}</table>
{{else}}
<p>No client has booted from this server yet, and none is armed.</p>
{{end}}
<p class="note">This page is read-only and refreshes every ten seconds. Clients are armed with <code>kyro arm</code> on the server.
A client is identified by what it says about itself, which is selection, not authentication.</p>
</body>
</html>
`))

// StatusHandler returns the read-only status page and API.
//
// It shows which machines exist and what was installed on them. Serve it
// on an address that only administrators reach.
func (s *Service) StatusHandler() http.Handler {
	list := func() ([]ClientView, error) {
		clients, err := s.State.List()
		if err != nil {
			return nil, err
		}
		out := make([]ClientView, len(clients))
		for i, c := range clients {
			out[i] = View(c)
		}
		return out, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		clients, err := list()
		if err != nil {
			s.Log.Error("status: cannot read the client state", "err", err)
			http.Error(w, "cannot read the client records; see the Kyro log", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		statusPage.Execute(w, struct{ Clients []ClientView }{clients})
	})
	mux.HandleFunc("GET /api/v1/clients", func(w http.ResponseWriter, r *http.Request) {
		clients, err := list()
		if err != nil {
			s.Log.Error("status: cannot read the client state", "err", err)
			http.Error(w, `{"error":"cannot read the client records"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if clients == nil {
			clients = []ClientView{}
		}
		json.NewEncoder(w).Encode(struct {
			Clients []ClientView `json:"clients"`
		}{clients})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	return mux
}
