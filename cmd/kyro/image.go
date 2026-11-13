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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/kyro-pxe/kyro/internal/images"
)

func (e *env) image(args []string) error {
	if len(args) == 0 {
		return usageErr("use 'kyro image add <iso>', 'kyro image list' or 'kyro image remove <name>'")
	}
	switch args[0] {
	case "add":
		return e.imageAdd(args[1:])
	case "list", "ls":
		return e.imageList(args[1:])
	case "remove", "rm":
		return e.imageRemove(args[1:])
	}
	return usageErr("unknown subcommand %q; use add, list or remove", args[0])
}

var notNameChar = regexp.MustCompile(`[^a-z0-9]+`)

// nameFromFile turns a file name into something that can be an image or
// profile name.
func nameFromFile(path string, max int) string {
	base := strings.ToLower(filepath.Base(path))
	for _, ext := range []string{".iso", ".kyro.json", ".json", ".xml"} {
		base = strings.TrimSuffix(base, ext)
	}
	name := strings.Trim(notNameChar.ReplaceAllString(base, "-"), "-")
	if len(name) > max {
		name = strings.TrimRight(name[:max], "-")
	}
	return name
}

func (e *env) imageAdd(args []string) error {
	c := e.command("image add", "<iso> [flags]", `Check a Windows ISO and add it to the images. The ISO is copied into the
data directory, or linked with --link. It is never unpacked or changed.

You supply the ISO. Kyro does not download or distribute Windows.
`)
	name := c.String("name", "", "name of the image (default: from the file name)")
	link := c.Bool("link", false, "make a symbolic link to the ISO instead of copying it; the service must be able to read it where it is")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one ISO file")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	src := pos[0]
	if *name == "" {
		*name = nameFromFile(src, 32)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !*link {
		if free, ok := freeBytes(cfg.ImagesDir()); ok && free < info.Size()+(256<<20) {
			return fmt.Errorf("%s has %s free and the ISO is %s; free some space or use --link", cfg.ImagesDir(), size(free), size(info.Size()))
		}
	}
	verb := "copying"
	if *link {
		verb = "reading"
	}
	last := -1
	im, err := images.Open(cfg.ImagesDir()).Add(src, *name, images.AddOptions{
		Link: *link,
		Progress: func(done, total int64) {
			if pct := int(done * 100 / max(total, 1)); pct/10 != last/10 || last < 0 {
				last = pct
				fmt.Fprintf(e.stderr, "\r%s %s: %d%%", verb, filepath.Base(src), pct)
			}
		},
	})
	if last >= 0 {
		fmt.Fprintln(e.stderr)
	}
	if errors.Is(err, images.ErrExists) {
		return fmt.Errorf("an image called %q exists; choose another with --name or remove it first", *name)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Image %s added (%s, sha256 %s).\n", im.Name, size(im.Size), im.SHA256)
	e.printEditions(im)
	return nil
}

func (e *env) printEditions(im images.Image) {
	if len(im.Editions) == 0 {
		fmt.Fprintln(e.stdout, "The editions in it could not be read. That does not stop it from installing.")
		return
	}
	fmt.Fprintln(e.stdout, "Editions in it (an answer file selects one by its index):")
	for _, ed := range im.Editions {
		fmt.Fprintf(e.stdout, "  %d  %s", ed.Index, ed.Name)
		if ed.Build != "" {
			fmt.Fprintf(e.stdout, " (build %s", ed.Build)
			if ed.Arch != "" {
				fmt.Fprintf(e.stdout, ", %s", ed.Arch)
			}
			fmt.Fprint(e.stdout, ")")
		}
		fmt.Fprintln(e.stdout)
	}
}

func (e *env) imageList(args []string) error {
	c := e.command("image list", "", "List the images.\n")
	if _, err := c.parse(args); err != nil {
		return err
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	list, err := images.Open(cfg.ImagesDir()).List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(e.stdout, "No images. Add one with 'kyro image add <iso>'.")
		return nil
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSIZE\tEDITIONS\tADDED")
	for _, im := range list {
		var eds []string
		for _, ed := range im.Editions {
			eds = append(eds, fmt.Sprintf("%d: %s", ed.Index, ed.Name))
		}
		if len(eds) == 0 {
			eds = []string{"(unknown)"}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", im.Name, size(im.Size), strings.Join(eds, "; "), im.AddedAt.Format("2006-01-02"))
	}
	return w.Flush()
}

func (e *env) imageRemove(args []string) error {
	c := e.command("image remove", "<name>", "Remove an image. A linked ISO stays where it is.\n")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one image")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	if err := images.Open(cfg.ImagesDir()).Remove(pos[0]); errors.Is(err, images.ErrNotFound) {
		return fmt.Errorf("there is no image called %q", pos[0])
	} else if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Image %s removed.\n", pos[0])
	return nil
}

// size writes a number of bytes the way people read it.
func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}
