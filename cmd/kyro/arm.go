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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kyro-pxe/kyro/internal/boot"
	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/drivers"
	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/state"
)

// selectionNotice is printed whenever a client is armed.
const selectionNotice = `The client is selected by what it says about itself. That is not
authentication: on a network where another machine can claim this
identifier, that machine gets the installation and everything in the
answer file.`

func (e *env) arm(args []string) error {
	c := e.command("arm", "<mac|uuid|serial> --image <image> --profile <profile> [flags]", `Install a client on its next network boot. This works once: after the
client has been given the installer, it boots from its disk again until it
is armed again.

An arming does not last for ever. If the client has not booted from the
network within the time of --expires (default: arm_timeout in the
configuration, 24h unless changed), it is not installed and shows as
expired.

The client is named by its MAC address, its SMBIOS UUID or its serial
number. 'kyro clients' lists the clients that have booted from this
server, with what they reported.
`)
	image := c.String("image", "", "image to install (required)")
	profile := c.String("profile", "", "profile to install with (required)")
	confirmWipe := c.Bool("confirm-wipe", false, "confirm that this installation may wipe a disk of the client")
	force := c.Bool("force", false, "arm a client that is being installed right now")
	expires := c.String("expires", "", "how long the arming lasts, like 2h or 7d (1m to 30d; default: arm_timeout)")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one client by its MAC address, UUID or serial number")
	}
	if *image == "" || *profile == "" {
		return usageErr("--image and --profile are required")
	}
	key, err := state.ParseKey(pos[0])
	if err != nil {
		return usageErr("%v", err)
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}

	lifetime := time.Duration(cfg.ArmTimeout)
	if *expires != "" {
		if lifetime, err = config.ParseDuration(*expires); err != nil {
			return usageErr("--expires: %v", err)
		}
		if lifetime < state.MinArmLifetime || lifetime > state.MaxArmLifetime {
			return usageErr("--expires %s: %v", *expires, state.ErrLifetime)
		}
	}

	im, err := images.Open(cfg.ImagesDir()).Get(*image)
	if errors.Is(err, images.ErrNotFound) {
		return fmt.Errorf("there is no image called %q; see 'kyro image list'", *image)
	}
	if err != nil {
		return err
	}
	_, p, err := profiles.Open(cfg.ProfilesDir()).AnswerFile(*profile)
	if errors.Is(err, profiles.ErrNotFound) {
		return fmt.Errorf("there is no profile called %q; see 'kyro profile list'", *profile)
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(e.stdout, "Profile %s:\n", p.Name)
	printProfile(e.stdout, p)
	if p.WipesDisk() && !*confirmWipe {
		fmt.Fprintf(e.stderr, "\nNot armed. This profile is treated as wiping a disk, and whatever is on\nthe disks of %s would be lost. If that is what you want, run the command\nagain with --confirm-wipe.\n", key)
		return errReported
	}

	st, err := state.Open(cfg.StateDir())
	if err != nil {
		return err
	}
	client, err := st.Arm(key, state.Arming{Image: im.Name, Profile: p.Name, Lifetime: lifetime}, *force)
	if errors.Is(err, state.ErrInstalling) {
		return fmt.Errorf("%s is being installed right now (since %s); use --force to arm it anyway", key, when(client.StartedAt))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "\nArmed: %s will be installed with image %s and profile %s on its next\nnetwork boot, once.\n", key, im.Name, p.Name)
	fmt.Fprintf(e.stdout, "The arming lasts until %s (%s). If the client has not booted from the\nnetwork by then, it is not installed.\n\n%s\n", when(client.ArmedUntil), lifetimeText(lifetime), selectionNotice)
	if packs, err := drivers.Open(cfg.DriversDir()).List(); err != nil {
		fmt.Fprintf(e.stdout, "\nNote: the driver packs cannot be read, so the installation will fail: %v\n", err)
	} else if len(packs) > 0 {
		names := make([]string, len(packs))
		for i, p := range packs {
			names[i] = p.Name
		}
		fmt.Fprintf(e.stdout, "\nDriver packs that the installation is given: %s.\n", strings.Join(names, ", "))
	}
	if cfg.SMB.Mode != "embedded" {
		fmt.Fprintln(e.stdout, "\nNote: the install share is switched off in the configuration (smb.mode), so the installation will fail.")
	}
	return nil
}

func (e *env) disarm(args []string) error {
	c := e.command("disarm", "<mac|uuid|serial>", `Take back an 'arm' that the client has not used yet.
`)
	forget := c.Bool("forget", false, "remove the record of the client altogether, whatever its state")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one client by its MAC address, UUID or serial number")
	}
	key, err := state.ParseKey(pos[0])
	if err != nil {
		return usageErr("%v", err)
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	st, err := state.Open(cfg.StateDir())
	if err != nil {
		return err
	}
	if *forget {
		if err := st.Forget(key); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "%s is forgotten.\n", key)
		return nil
	}
	client, err := st.Disarm(key)
	if errors.Is(err, state.ErrNotArmed) {
		return fmt.Errorf("%s is not armed (its state is %s)", key, client.State)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "%s is disarmed. It boots from its disk.\n", key)
	return nil
}

// lifetimeText writes a lifetime the way it is typed: 36h, 7d, 90m.
func lifetimeText(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func (e *env) clients(args []string) error {
	c := e.command("clients", "[flags]", `List the clients: those that are armed, those that were installed, and those
that booted from this server without being armed.
`)
	asJSON := c.Bool("json", false, "print the list as JSON")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr("unexpected argument %q", pos[0])
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	st, err := state.Open(cfg.StateDir())
	if err != nil {
		return err
	}
	// Installations and armings that have run out of time show as failed
	// and expired even if the service is not running to notice.
	if _, err := st.Expire(time.Duration(cfg.InstallTimeout)); err != nil {
		return err
	}
	list, err := st.List()
	if err != nil {
		return err
	}
	if *asJSON {
		views := make([]boot.ClientView, len(list))
		for i, cl := range list {
			views[i] = boot.View(cl)
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Clients []boot.ClientView `json:"clients"`
		}{views})
	}
	if len(list) == 0 {
		fmt.Fprintln(e.stdout, "No clients yet. A client shows up here when it boots from this server, or when you arm it.")
		return nil
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CLIENT\tSTATE\tIMAGE\tPROFILE\tLAST SEEN\tMAC\tSERIAL\tNOTE")
	for _, cl := range list {
		v := boot.View(cl)
		note := v.Failure
		if cl.State == state.Armed && v.ArmedUntil != nil {
			note = "armed until " + when(v.ArmedUntil)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Client, v.State, dash(v.Image), dash(v.Profile), when(v.LastSeen), dash(v.MAC), dash(v.Serial), note)
	}
	return w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
