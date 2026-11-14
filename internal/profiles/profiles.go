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

// Package profiles stores answer files and renders them for a client.
//
// A profile is an answer file (autounattend.xml) with a little metadata.
// Kyro stores, checks and serves profiles. It does not generate answer
// files and does not interpret what is in them, with one exception: it
// looks for signs that a file changes a disk, because arming a client for
// such a file needs an explicit confirmation.
//
// See docs/adr/0002-profiles.md.
package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kyro-pxe/kyro/internal/fsutil"
)

// BundleVersion is the version of the bundle format that this Kyro reads.
//
// docs/adr/0002-profiles.md is the specification; the generator on the Kyro
// website is built from it. The version is 0 while Kyro is an alpha and the
// format may still change from one release to the next.
const BundleVersion = 0

// Bundle is the JSON document in which profiles are exchanged.
type Bundle struct {
	BundleVersion *int `json:"bundle_version"`
	// Name is the profile name. The person importing can override it.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// WipesDisk says whether the answer file partitions, formats or wipes
	// a disk without asking. It must be present.
	WipesDisk *bool `json:"wipes_disk"`
	// Warnings are written by whoever made the bundle, for example that
	// the file contains a password. Kyro shows them and cannot verify them.
	Warnings []string `json:"warnings,omitempty"`
	// WindowsBuilds lists the build numbers the answer file was made for.
	WindowsBuilds []string `json:"windows_builds,omitempty"`
	// AnswerFile is the content of autounattend.xml.
	AnswerFile string `json:"answer_file"`
}

// Profile is the metadata of a stored profile.
type Profile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Source is "bundle" or "xml".
	Source string `json:"source"`
	// DeclaredWipe is what the bundle said about wiping disks. It is nil
	// for a bare answer file, which says nothing.
	DeclaredWipe *bool `json:"declared_wipe"`
	// DetectedWipe lists what Kyro found in the answer file that suggests
	// it changes a disk.
	DetectedWipe  []string `json:"detected_wipe,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	WindowsBuilds []string `json:"windows_builds,omitempty"`
	// Tokens are the tokens the answer file uses.
	Tokens     []string  `json:"tokens,omitempty"`
	SHA256     string    `json:"sha256"`
	ImportedAt time.Time `json:"imported_at"`
}

// WipesDisk reports whether arming a client for this profile needs the
// confirmation that a disk may be wiped. It does unless a bundle says that
// the file leaves disks alone and Kyro found nothing to the contrary. A
// bare answer file declares nothing, so it always needs the confirmation.
func (p Profile) WipesDisk() bool {
	return p.DeclaredWipe == nil || *p.DeclaredWipe || len(p.DetectedWipe) > 0
}

// WipeReason says in one sentence why WipesDisk is true.
func (p Profile) WipeReason() string {
	switch {
	case p.DeclaredWipe != nil && *p.DeclaredWipe:
		return "the bundle says that it wipes a disk"
	case len(p.DetectedWipe) > 0:
		return "the answer file looks like it changes a disk: " + strings.Join(p.DetectedWipe, "; ")
	case p.DeclaredWipe == nil:
		return "it is a bare answer file, and Kyro cannot tell what it does to disks"
	}
	return ""
}

// Import is a profile that has passed all checks and is ready to be
// stored.
type Import struct {
	Profile    Profile
	AnswerFile []byte
	// Notes are things the person importing should know, such as tokens
	// that will not be replaced.
	Notes []string
}

var (
	nameRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	buildRE = regexp.MustCompile(`^[0-9]{4,6}$`)
)

// ValidName reports whether s can be a profile name. Names are used as
// directory names, in URLs and in menus.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// Parse checks a bundle or a bare answer file. Which of the two it is, is
// decided by its first character, not by a file name. name overrides the
// name in a bundle and is required for a bare answer file.
func Parse(data []byte, name string) (*Import, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("the file is larger than %d bytes", MaxSize)
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return nil, errors.New("the file is UTF-16; save it as UTF-8")
	}
	body := bytes.TrimLeft(bytes.TrimPrefix(data, utf8BOM), " \t\r\n")
	if len(body) == 0 {
		return nil, errors.New("the file is empty")
	}
	imp := &Import{}
	switch body[0] {
	case '{':
		b, err := parseBundle(bytes.TrimPrefix(data, utf8BOM))
		if err != nil {
			return nil, err
		}
		imp.Profile = Profile{
			Name:          b.Name,
			Description:   clean(b.Description),
			Source:        "bundle",
			DeclaredWipe:  b.WipesDisk,
			WindowsBuilds: b.WindowsBuilds,
		}
		for _, w := range b.Warnings {
			if w = clean(w); w != "" {
				imp.Profile.Warnings = append(imp.Profile.Warnings, w)
			}
		}
		imp.AnswerFile = []byte(b.AnswerFile)
	case '<':
		imp.Profile.Source = "xml"
		imp.AnswerFile = data
	default:
		return nil, errors.New("the file is neither a Kyro bundle (JSON) nor an answer file (XML)")
	}
	if name != "" {
		imp.Profile.Name = name
	}
	if imp.Profile.Name == "" {
		return nil, errors.New("the profile needs a name")
	}
	if !ValidName(imp.Profile.Name) {
		return nil, fmt.Errorf("%q cannot be a profile name: use 1 to 63 lower-case letters, digits and hyphens, not starting with a hyphen", clean(imp.Profile.Name))
	}

	res, err := scan(imp.AnswerFile)
	if err != nil {
		return nil, err
	}
	imp.Notes = res.notes
	imp.Profile.DetectedWipe = res.wipes
	used := map[string]bool{}
	for _, t := range res.tokens {
		used[t.name] = true
	}
	for n := range used {
		imp.Profile.Tokens = append(imp.Profile.Tokens, n)
	}
	sort.Strings(imp.Profile.Tokens)
	sum := sha256.Sum256(imp.AnswerFile)
	imp.Profile.SHA256 = hex.EncodeToString(sum[:])
	if d := imp.Profile.DeclaredWipe; d != nil && !*d && len(res.wipes) > 0 {
		imp.Notes = append(imp.Notes, "the bundle says that it does not wipe a disk, but "+imp.Profile.WipeReason()+"; Kyro treats it as wiping")
	}
	return imp, nil
}

func parseBundle(data []byte) (*Bundle, error) {
	// The version is read first, so that a bundle from a newer generator
	// gets a message about its version and not about an unknown field.
	var head struct {
		BundleVersion json.RawMessage `json:"bundle_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		// A second document after the first is reported further down.
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) || !strings.Contains(err.Error(), "after top-level value") {
			return nil, fmt.Errorf("the bundle is not valid JSON: %v", err)
		}
		json.NewDecoder(bytes.NewReader(data)).Decode(&head)
	}
	if head.BundleVersion == nil {
		return nil, errors.New("the file is JSON but has no bundle_version, so it is not a Kyro bundle")
	}
	if string(head.BundleVersion) != fmt.Sprint(BundleVersion) {
		return nil, fmt.Errorf("the bundle has version %s; this Kyro reads version %d", clean(string(head.BundleVersion)), BundleVersion)
	}

	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("the bundle does not have the expected fields: %v", err)
	}
	if dec.More() {
		return nil, errors.New("the bundle has data after its JSON document")
	}
	if b.WipesDisk == nil {
		return nil, errors.New("the bundle does not say whether it wipes a disk (wipes_disk)")
	}
	if b.AnswerFile == "" {
		return nil, errors.New("the bundle has no answer_file")
	}
	if len(b.Description) > 2000 {
		return nil, errors.New("the description is longer than 2000 bytes")
	}
	if len(b.Warnings) > 32 {
		return nil, errors.New("the bundle has more than 32 warnings")
	}
	for _, w := range b.Warnings {
		if len(w) > 1000 {
			return nil, errors.New("a warning is longer than 1000 bytes")
		}
	}
	if len(b.WindowsBuilds) > 64 {
		return nil, errors.New("the bundle lists more than 64 Windows builds")
	}
	for _, build := range b.WindowsBuilds {
		if !buildRE.MatchString(build) {
			return nil, fmt.Errorf("%q is not a Windows build number such as 26100", clean(build))
		}
	}
	return &b, nil
}

// Store is a directory of profiles, one subdirectory each:
//
//	<name>/autounattend.xml   the answer file, byte for byte as imported
//	<name>/profile.json       the metadata
type Store struct {
	dir string
}

// Open returns the store in dir.
func Open(dir string) *Store { return &Store{dir: dir} }

// ErrExists means that a profile of that name is already stored.
var ErrExists = errors.New("a profile of that name exists")

// ErrNotFound means that no profile of that name is stored.
var ErrNotFound = errors.New("no such profile")

// Add stores a checked profile. With replace it overwrites a profile of
// the same name.
func (s *Store) Add(imp *Import, replace bool) error {
	if !ValidName(imp.Profile.Name) {
		return fmt.Errorf("invalid profile name %q", imp.Profile.Name)
	}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return err
	}
	final := filepath.Join(s.dir, imp.Profile.Name)
	if _, err := os.Stat(final); err == nil && !replace {
		return ErrExists
	}
	// Build the profile next to its final place and move it there in one
	// step, so that a half-written profile is never visible.
	tmp, err := os.MkdirTemp(s.dir, ".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o750); err != nil {
		return err
	}
	fsutil.OwnLike(tmp, s.dir)

	p := imp.Profile
	p.ImportedAt = time.Now().UTC().Truncate(time.Second)
	meta, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	// Answer files can contain passwords: not readable by everyone.
	if err := fsutil.WriteFile(filepath.Join(tmp, "autounattend.xml"), imp.AnswerFile, 0o640); err != nil {
		return err
	}
	if err := fsutil.WriteFile(filepath.Join(tmp, "profile.json"), append(meta, '\n'), 0o640); err != nil {
		return err
	}
	if replace {
		old := tmp + ".old"
		if err := os.Rename(final, old); err == nil {
			defer os.RemoveAll(old)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return err
	}
	fsutil.SyncDir(s.dir)
	return nil
}

// Get returns the metadata of a profile.
func (s *Store) Get(name string) (Profile, error) {
	if !ValidName(name) {
		return Profile{}, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name, "profile.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	if p.Name != name {
		return Profile{}, fmt.Errorf("profile %s: its metadata is for %q", name, clean(p.Name))
	}
	return p, nil
}

// AnswerFile returns the answer file of a profile as it was imported. It is
// checked against the checksum in the metadata, so a file that was changed
// behind Kyro's back is not served.
func (s *Store) AnswerFile(name string) ([]byte, Profile, error) {
	p, err := s.Get(name)
	if err != nil {
		return nil, Profile{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name, "autounattend.xml"))
	if err != nil {
		return nil, Profile{}, err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != p.SHA256 {
		return nil, Profile{}, fmt.Errorf("profile %s: the answer file was changed after it was imported; import it again", name)
	}
	return data, p, nil
}

// List returns all profiles, sorted by name.
func (s *Store) List() ([]Profile, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		p, err := s.Get(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Remove deletes a profile.
func (s *Store) Remove(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dir, name))
}
