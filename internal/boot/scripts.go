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
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kyro-pxe/kyro/internal/drivers"
	"github.com/kyro-pxe/kyro/internal/state"
)

// ShareName is the name of the install share. Every image is a directory
// in it.
const ShareName = "install"

// The scripts in this file are sent to machines and run there. Whatever is
// written into them is checked first: an address, names of images and
// profiles, the share account and paths out of an ISO all have a limited
// set of characters, and none of them can end a line or start a command.

var (
	scriptWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	scriptPath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	scriptBase = regexp.MustCompile(`^http://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
)

// bootstrapScript is what iPXE loads first. It asks again with the
// identifiers of the machine, which DHCP did not carry.
func bootstrapScript(base string) string {
	return "#!ipxe\n" +
		"# Kyro: ask what this machine should do.\n" +
		"chain " + base + "/boot?mac=${netX/mac:hexhyp}&uuid=${uuid}&serial=${serial:uristring} || exit 1\n"
}

// localScript sends a machine to its own disk.
//
// "exit 1" and not "exit": a UEFI boot manager goes on to the next boot
// entry only if the one before it failed. After a plain "exit" some of them
// show their menu instead.
func localScript(why string) string {
	return "#!ipxe\n" +
		"echo Kyro: " + why + " Booting from the local disk.\n" +
		"exit 1\n"
}

// menuScript shows a machine that is not armed what it would have to be
// armed by. Nothing in the menu changes the machine.
func menuScript() string {
	return `#!ipxe
menu Kyro: this machine is not armed for an installation
item --gap -- MAC address    ${netX/mac}
item --gap -- UUID           ${uuid}
item --gap -- Serial number  ${serial}
item --gap --
item --gap -- To install it, arm it on the Kyro server and boot it again.
item --gap --
item local    Boot from the local disk
item reboot   Restart
choose --timeout 20000 --default local target || goto local
goto ${target}
:reboot
reboot
:local
exit 1
`
}

// installScript loads Windows PE with the files of one installation. With
// drivers it also loads the cabinet of driver packs.
func installScript(base, token, image, profile string, drivers bool) (string, error) {
	// The errors name what is wrong and never quote the token: they are
	// logged and kept in the client's record, and the token is the one
	// thing that opens the files of this installation.
	switch {
	case !scriptBase.MatchString(base):
		return "", fmt.Errorf("refusing to write the address %q into a boot script", base)
	case !scriptWord.MatchString(token):
		return "", errors.New("refusing to write the installation token into a boot script: it has characters that do not belong in one")
	case !scriptWord.MatchString(image):
		return "", fmt.Errorf("refusing to write the image name %q into a boot script", image)
	case !scriptWord.MatchString(profile):
		return "", fmt.Errorf("refusing to write the profile name %q into a boot script", profile)
	}
	dir := base + "/i/" + token + "/"
	script := "#!ipxe\n" +
		"echo Kyro: installing image " + image + " with profile " + profile + "\n" +
		"kernel " + base + "/wimboot\n" +
		"initrd " + dir + "winpeshl.ini winpeshl.ini\n" +
		"initrd " + dir + "kyro-start.cmd kyro-start.cmd\n" +
		"initrd " + dir + "autounattend.xml autounattend.xml\n"
	if drivers {
		script += "initrd " + dir + driversCabinet + " " + driversCabinet + "\n"
	}
	return script +
		"initrd " + dir + "boot.wim boot.wim\n" +
		"boot || exit 1\n", nil
}

// driversCabinet is the name of the cabinet of driver packs, in the URL
// and in Windows PE, where wimboot puts it into X:\Windows\System32.
const driversCabinet = drivers.CabinetName

// driversDir is where the start script unpacks the driver packs. Windows
// Setup looks for a directory of this name on every drive and adds the
// drivers it finds there to the system it installs.
const driversDir = `X:\$WinPEDriver$`

// ReportPrefix starts the names under which the start script reports on
// the install share: kyro-report.<token>.<event>, and for a failure of
// Setup kyro-report.<token>.setup-failed.<exit code>. Names of images have
// no dots, so no image can be called like that.
const ReportPrefix = "kyro-report."

// winpeshl is the file that makes Windows PE run Kyro's start script in
// place of Setup.
const winpeshl = "[LaunchApps]\r\n%SYSTEMROOT%\\System32\\cmd.exe, /c %SYSTEMROOT%\\System32\\kyro-start.cmd\r\n"

// startParams is what the start script needs to know.
type startParams struct {
	// Token is the token of the installation. The script reports its
	// progress under it.
	Token    string
	Address  string // Kyro's address
	Port     int    // of the SMB server
	Username string
	Password string
	Image    string   // directory of the image in the share
	Install  string   // path of the install image inside the ISO
	Extras   []string // paths of ei.cfg and pid.txt inside the ISO
}

// startScript returns the batch file that runs in Windows PE: connect the
// install share, then start Setup from the RAM disk with the image on the
// share and the answer file that wimboot put next to this script.
func startScript(p startParams) ([]byte, error) {
	// The password is named, not quoted: this error is logged.
	for _, w := range []struct{ what, value string }{
		{"the address", p.Address}, {"the user name", p.Username}, {"the image name", p.Image},
	} {
		if !scriptWord.MatchString(w.value) {
			return nil, fmt.Errorf("refusing to write %s %q into the start script", w.what, w.value)
		}
	}
	if !scriptWord.MatchString(p.Password) {
		return nil, errors.New("refusing to write the password of the install share into the start script: it has characters that do not belong in one")
	}
	if !scriptWord.MatchString(p.Token) {
		return nil, errors.New("refusing to write the installation token into the start script: it has characters that do not belong in one")
	}
	for _, path := range append([]string{p.Install}, p.Extras...) {
		if !scriptPath.MatchString(path) {
			return nil, fmt.Errorf("refusing to write the path %q into the start script", path)
		}
	}
	if p.Port < 1 || p.Port > 65535 {
		return nil, fmt.Errorf("invalid SMB port %d", p.Port)
	}
	win := func(path string) string { return strings.ReplaceAll(path, "/", `\`) }

	netUse := `net use %KYRO_SHARE% /user:` + p.Username + ` ` + p.Password
	if p.Port != 445 {
		// Windows 11 24H2 and later can use another port. Older Windows
		// PE cannot, and fails here.
		netUse += fmt.Sprintf(" /TCPPORT:%d", p.Port)
	}
	// A report: the script tries to read a file on the share whose name
	// says what happened. There is no such file; Kyro notes that it was
	// asked for. Windows PE has no program that could send an HTTP request.
	// The parentheses are needed: cmd.exe complains about the missing file
	// before it gets to a "2>nul" on the same command, and that complaint
	// on the screen looks like an error of the installation.
	report := func(event string) string {
		return `(set /p KYRO_NONE=<%KYRO_REPORT%.` + event + `) 2>nul`
	}
	lines := []string{
		`@echo off`,
		`rem Written by Kyro for one installation. Windows PE runs this in place of`,
		`rem Setup. It connects the install share and starts Setup from there.`,
		`set KYRO_SHARE=\\` + p.Address + `\` + ShareName,
		`set KYRO_SRC=%KYRO_SHARE%\` + p.Image,
		`set KYRO_REPORT=%KYRO_SHARE%\` + ReportPrefix + p.Token,
		`echo.`,
		`echo Kyro: starting the network`,
		`wpeinit`,
		// Driver packs, if this installation was given any. They are
		// loaded now, so that the network card works before the share is
		// connected, and they stay where Setup looks for drivers to add to
		// the installed system.
		`if not exist X:\Windows\System32\` + driversCabinet + ` goto nodrivers`,
		`echo Kyro: loading drivers`,
		`mkdir "` + driversDir + `" >nul 2>&1`,
		`expand X:\Windows\System32\` + driversCabinet + ` -F:* "` + driversDir + `" >nul`,
		`if errorlevel 1 echo Kyro: the driver packs could not be unpacked`,
		`for /r "` + driversDir + `" %%i in (*.inf) do drvload "%%i"`,
		`wpeutil InitializeNetwork >nul`,
		`:nodrivers`,
		`wpeutil WaitForNetwork >nul`,
		`set KYRO_TRIES=0`,
		`:connect`,
		netUse + ` >nul 2>&1`,
		`if not errorlevel 1 goto connected`,
		`set /a KYRO_TRIES+=1`,
		`if %KYRO_TRIES% geq 40 goto noshare`,
		`ping -n 4 127.0.0.1 >nul`,
		`goto connect`,
		`:connected`,
		`echo Kyro: connected to %KYRO_SHARE%`,
		report(state.EventShareConnected),
	}
	// Setup started from the RAM disk does not look for these two on the
	// share. They decide the edition and the product key.
	for _, extra := range p.Extras {
		name := strings.ToLower(extra[strings.LastIndex(extra, "/")+1:])
		lines = append(lines, `copy /y %KYRO_SRC%\`+win(extra)+` X:\sources\`+name+` >nul`)
	}
	lines = append(lines,
		`echo Kyro: starting Windows Setup`,
		report(state.EventSetupStarted),
		`X:\sources\setup.exe /installfrom:%KYRO_SRC%\`+win(p.Install)+` /unattend:X:\Windows\System32\autounattend.xml`,
		`if errorlevel 1 goto setupfailed`,
		report(state.EventSetupFinished),
		`wpeutil reboot`,
		`goto :eof`,
		`:noshare`,
		`echo.`,
		`echo Kyro: could not connect to %KYRO_SHARE%. The last attempt said:`,
		netUse,
		`goto stopped`,
		`:setupfailed`,
		`set KYRO_RC=%ERRORLEVEL%`,
		report(state.EventSetupFailed+`.%KYRO_RC%`),
		`echo.`,
		// 1223 is what Setup returns when a person closes its window.
		`if "%KYRO_RC%"=="`+state.SetupCancelled+`" goto setupclosed`,
		`echo Kyro: Windows Setup ended with an error. Its logs are in X:\Windows\Panther.`,
		`goto stopped`,
		`:setupclosed`,
		`echo Kyro: Windows Setup was closed.`,
		`:stopped`,
		`echo Kyro: this installation has stopped. Arm the client again to retry.`,
		`cmd.exe /k`,
	)
	return []byte(strings.Join(lines, "\r\n") + "\r\n"), nil
}
