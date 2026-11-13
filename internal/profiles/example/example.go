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

// Package example holds the one profile that ships with Kyro.
//
// It is a test fixture: it wipes a disk and creates an account whose
// password is public. It exists for the end-to-end test and as a working
// first example. See the comment at the top of autounattend.xml.
package example

import (
	_ "embed"
	"encoding/json"

	"github.com/kyro-pxe/kyro/internal/profiles"
)

// Name is the name under which the example is imported.
const Name = "kyro-example"

//go:embed autounattend.xml
var answerFile string

// Bundle returns the example as a bundle, ready for profiles.Parse.
func Bundle() []byte {
	version, wipes := profiles.BundleVersion, true
	data, err := json.MarshalIndent(profiles.Bundle{
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
	}, "", "  ")
	if err != nil {
		panic(err) // the bundle is made of constants
	}
	return data
}
