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
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/profiles/example"
)

func (e *env) profile(args []string) error {
	if len(args) == 0 {
		return usageErr("use 'kyro profile import <file>', 'list', 'show <name>' or 'remove <name>'")
	}
	switch args[0] {
	case "import":
		return e.profileImport(args[1:])
	case "list", "ls":
		return e.profileList(args[1:])
	case "show":
		return e.profileShow(args[1:])
	case "remove", "rm":
		return e.profileRemove(args[1:])
	}
	return usageErr("unknown subcommand %q; use import, list, show or remove", args[0])
}

// reviewNotice is printed on every import. Kyro cannot judge an answer
// file, whatever a bundle says about itself.
const reviewNotice = `Answer files can wipe disks and contain passwords. Kyro checks that this one
is well-formed, not what it does. Read it before you use it.`

// printProfile shows what an administrator should know about a profile
// before using it.
func printProfile(w io.Writer, p profiles.Profile) {
	if p.Description != "" {
		fmt.Fprintf(w, "  Description: %s\n", p.Description)
	}
	if len(p.WindowsBuilds) > 0 {
		fmt.Fprintf(w, "  Made for Windows builds: %s\n", strings.Join(p.WindowsBuilds, ", "))
	}
	if len(p.Tokens) > 0 {
		fmt.Fprintf(w, "  Filled in per client: {{%s}}\n", strings.Join(p.Tokens, "}}, {{"))
	}
	if p.WipesDisk() {
		fmt.Fprintf(w, "  DISKS: treated as wiping a disk, because %s.\n", p.WipeReason())
		fmt.Fprintln(w, "         Arming a client for it needs --confirm-wipe.")
	} else {
		fmt.Fprintln(w, "  Disks: the bundle says that it leaves disks alone, and Kyro found nothing to the contrary.")
	}
	for _, warning := range p.Warnings {
		fmt.Fprintf(w, "  WARNING from the bundle: %s\n", warning)
	}
	if p.Source == "bundle" && len(p.Warnings) == 0 {
		fmt.Fprintln(w, "  The bundle carries no warnings. That is not a statement that it is harmless.")
	}
}

func (e *env) profileImport(args []string) error {
	c := e.command("profile import", "<file> [flags]", `Check a profile and store it. The file is a Kyro bundle (JSON) or a bare
answer file (autounattend.xml); which one is decided by its content.

--example imports the profile that ships with Kyro. It is a test fixture:
it wipes disk 0 and creates an account with a public password.
`)
	name := c.String("name", "", "name of the profile (default: from the bundle, or from the file name)")
	replace := c.Bool("replace", false, "overwrite a profile of the same name")
	useExample := c.Bool("example", false, "import the built-in example profile instead of a file")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	var data []byte
	switch {
	case *useExample && len(pos) == 0:
		data = example.Bundle()
	case !*useExample && len(pos) == 1:
		f, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		defer f.Close()
		// One byte over the limit is enough to know that it is too large.
		if data, err = io.ReadAll(io.LimitReader(f, profiles.MaxSize+1)); err != nil {
			return err
		}
	default:
		return usageErr("name one file, or use --example")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}

	imp, err := profiles.Parse(data, *name)
	if err != nil && *name == "" && len(pos) == 1 && strings.Contains(err.Error(), "needs a name") {
		// A bare answer file, or a bundle without a name: take the file's.
		imp, err = profiles.Parse(data, nameFromFile(pos[0], 63))
	}
	if err != nil {
		return fmt.Errorf("not imported: %w", err)
	}
	if err := profiles.Open(cfg.ProfilesDir()).Add(imp, *replace); errors.Is(err, profiles.ErrExists) {
		return fmt.Errorf("a profile called %q exists; use --replace, or another --name", imp.Profile.Name)
	} else if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Profile %s imported.\n", imp.Profile.Name)
	printProfile(e.stdout, imp.Profile)
	for _, note := range imp.Notes {
		fmt.Fprintf(e.stdout, "  Note: %s\n", note)
	}
	fmt.Fprintf(e.stdout, "\n%s\n", reviewNotice)
	return nil
}

func (e *env) profileList(args []string) error {
	c := e.command("profile list", "", "List the profiles.\n")
	if _, err := c.parse(args); err != nil {
		return err
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	list, err := profiles.Open(cfg.ProfilesDir()).List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(e.stdout, "No profiles. Import one with 'kyro profile import <file>', or the example with 'kyro profile import --example'.")
		return nil
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tWIPES DISK\tWARNINGS\tIMPORTED\tDESCRIPTION")
	for _, p := range list {
		wipes := "no"
		if p.WipesDisk() {
			wipes = "yes"
		}
		desc := p.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", p.Name, wipes, len(p.Warnings), p.ImportedAt.Format("2006-01-02"), desc)
	}
	return w.Flush()
}

func (e *env) profileShow(args []string) error {
	c := e.command("profile show", "<name> [flags]", "Show what Kyro knows about a profile.\n")
	xml := c.Bool("xml", false, "print the answer file as it was imported, and nothing else")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one profile")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	data, p, err := profiles.Open(cfg.ProfilesDir()).AnswerFile(pos[0])
	if errors.Is(err, profiles.ErrNotFound) {
		return fmt.Errorf("there is no profile called %q", pos[0])
	}
	if err != nil {
		return err
	}
	if *xml {
		_, err := e.stdout.Write(data)
		return err
	}
	fmt.Fprintf(e.stdout, "Profile %s\n", p.Name)
	fmt.Fprintf(e.stdout, "  Imported: %s, from a %s\n", p.ImportedAt.Format("2006-01-02 15:04 MST"), map[string]string{"bundle": "bundle", "xml": "bare answer file"}[p.Source])
	fmt.Fprintf(e.stdout, "  Answer file: %d bytes, sha256 %s\n", len(data), p.SHA256)
	printProfile(e.stdout, p)
	return nil
}

func (e *env) profileRemove(args []string) error {
	c := e.command("profile remove", "<name>", "Remove a profile.\n")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one profile")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	if err := profiles.Open(cfg.ProfilesDir()).Remove(pos[0]); errors.Is(err, profiles.ErrNotFound) {
		return fmt.Errorf("there is no profile called %q", pos[0])
	} else if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Profile %s removed.\n", pos[0])
	return nil
}
