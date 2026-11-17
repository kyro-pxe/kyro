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

// Package example holds the profiles that ship with Kyro.
//
// The first is a test fixture: it wipes a disk and creates an account whose
// password is public. It exists for the end-to-end test and as a working
// first example. See the comment at the top of autounattend.xml.
//
// The second, "interactive", answers none of Setup's questions. It exists
// for trying Kyro and driver packs on a machine whose disk has to stay as
// it is. See the comment at the top of interactive.xml.
package example

import (
	_ "embed"
	"encoding/json"

	"github.com/kyro-pxe/kyro/internal/profiles"
)

// Name is the name under which the example is imported.
const Name = "kyro-example"

// InteractiveName is the name under which the interactive example is
// imported.
const InteractiveName = "kyro-example-interactive"

//go:embed autounattend.xml
var answerFile string

//go:embed interactive.xml
var interactiveFile string

// Bundle returns the example as a bundle, ready for profiles.Parse.
func Bundle() []byte {
	version, wipes := profiles.BundleVersion, true
	return marshal(profiles.Bundle{
		BundleVersion: &version,
		Name:          Name,
		Description:   "TEST FIXTURE, not for real machines. Unattended Windows 11 on UEFI, made for the Enterprise evaluation ISO.",
		WipesDisk:     &wipes,
		Warnings: []string{
			"Wipes disk 0 without asking.",
			`Creates the local administrator "kyro-test" with the password "kyro-test-only", which is public, and logs it on automatically once.`,
			"Installs the first edition in the image, without a product key.",
		},
		AnswerFile: answerFile,
	})
}

// Interactive returns the interactive example as a bundle, ready for
// profiles.Parse. It declares that it leaves disks alone: the answer file
// has no disk section and no command, so Setup asks a person which disk to
// use, if any.
func Interactive() []byte {
	version, wipes := profiles.BundleVersion, false
	return marshal(profiles.Bundle{
		BundleVersion: &version,
		Name:          InteractiveName,
		Description:   "Starts Windows Setup and answers none of its questions. For trying Kyro and driver packs on a machine whose disk has to stay as it is.",
		WipesDisk:     &wipes,
		Warnings: []string{
			"Not an unattended installation: Windows Setup asks everything on the screen and waits there.",
			"Kyro wipes nothing with this profile, but a person at the machine can: Setup's disk page deletes and formats what that person chooses there.",
			"Windows PE reads the disks of the machine as a Windows installation USB stick does, and writes its own bookkeeping into NTFS volumes it finds. Partitions and files stay as they are.",
		},
		AnswerFile: interactiveFile,
	})
}

func marshal(b profiles.Bundle) []byte {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		panic(err) // the bundles are made of constants
	}
	return data
}
