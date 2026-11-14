#!/bin/sh
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Runs inside a fresh container, started by run.sh. /release holds the
# release files, /bootfiles holds wimboot and ipxeboot.tar.gz.
#
# Tests install.sh without systemd: refusing what was not verified,
# installing, running twice, uninstalling, purging.

set -eu

TARBALL=$(ls /release/kyro_*_linux_amd64.tar.gz)
SUMS=/release/SHA256SUMS
INSTALL=/release/install.sh
checks=0

pass() {
	checks=$((checks + 1))
	echo "PASS: $*"
}
fail() {
	echo "FAILED: $*" >&2
	exit 1
}
# refused runs a command that must fail and must say the given text.
refused() { # text command...
	want=$1
	shift
	if out=$("$@" 2>&1); then
		fail "'$*' was accepted"
	fi
	case "$out" in
	*"$want"*) ;;
	*) fail "'$*' failed without saying '$want': $out" ;;
	esac
}
nothing_installed() {
	[ ! -e /usr/local/bin/kyro ] || fail "$1: /usr/local/bin/kyro exists"
	[ ! -e /etc/kyro ] || fail "$1: /etc/kyro exists"
	[ ! -e /var/lib/kyro ] || fail "$1: /var/lib/kyro exists"
	if id kyro >/dev/null 2>&1; then fail "$1: the user kyro exists"; fi
}
mode_of() { stat -c '%U:%G %a' "$1"; }
expect() { # description got want
	[ "$2" = "$3" ] || fail "$1: got '$2', wanted '$3'"
}

# The address of this container, found without the program "ip", which a
# minimal container does not have.
ADDR=$(awk -v h="$(cat /etc/hostname)" '$2 == h { print $1; exit }' /etc/hosts)
[ -n "$ADDR" ] || fail "could not find the address of this container"
IFACE=eth0

# shellcheck disable=SC1091
echo "== $(. /etc/os-release && echo "$PRETTY_NAME"), address $ADDR"

# --- what must be refused -------------------------------------------------

refused "belongs to no release" sh /src/install.sh --yes
nothing_installed "source copy without a version"
pass "the copy from the source tree refuses to install without a named release"

refused "needs --checksums" sh "$INSTALL" --yes --tarball "$TARBALL"
nothing_installed "tarball without checksum"
pass "a tarball without a checksum is refused"

refused "CHECKSUM MISMATCH" sh "$INSTALL" --yes --tarball "$TARBALL" \
	--sha256 0000000000000000000000000000000000000000000000000000000000000000
nothing_installed "wrong checksum"
pass "a tarball with the wrong checksum is refused"

cp "$TARBALL" /tmp/
echo tampered >>"/tmp/$(basename "$TARBALL")"
refused "CHECKSUM MISMATCH" sh "$INSTALL" --yes --tarball "/tmp/$(basename "$TARBALL")" --checksums "$SUMS"
nothing_installed "changed tarball"
pass "a changed tarball is refused"

echo "0000  other.tar.gz" >/tmp/SUMS-other
refused "has no checksum for" sh "$INSTALL" --yes --tarball "$TARBALL" --checksums /tmp/SUMS-other
pass "a checksum file that does not list the tarball is refused"

refused "not implemented" sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" \
	--interface "$IFACE" --address "$ADDR" --dhcp-mode full --bootfiles-from /bootfiles
pass "DHCP mode full is refused"
nothing_installed "DHCP mode full"

if ! command -v ip >/dev/null 2>&1; then
	refused "name it with --interface" sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" --bootfiles-from /bootfiles
	nothing_installed "no interface"
	pass "without a way to find the interface, the installer asks for --interface and changes nothing"
fi

refused "add --yes" sh "$INSTALL" --purge </dev/null
pass "--purge without --yes is refused when nobody can be asked"

# --- installing -----------------------------------------------------------

sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" \
	--interface "$IFACE" --address "$ADDR" --bootfiles-from /bootfiles >/tmp/install1.log 2>&1 ||
	{
		cat /tmp/install1.log
		fail "the installation failed"
	}
pass "non-interactive installation"

kyro version >/dev/null || fail "kyro does not run"
id kyro >/dev/null 2>&1 || fail "no user kyro"
case "$(getent passwd kyro | cut -d: -f7)" in
*/nologin | */false) ;;
*) fail "the user kyro has a login shell: $(getent passwd kyro | cut -d: -f7)" ;;
esac
expect "/etc/kyro" "$(mode_of /etc/kyro)" "root:kyro 750"
expect "config.yaml" "$(mode_of /etc/kyro/config.yaml)" "root:kyro 640"
expect "/var/lib/kyro" "$(mode_of /var/lib/kyro)" "root:kyro 750"
expect "images" "$(mode_of /var/lib/kyro/images)" "root:kyro 750"
expect "profiles" "$(mode_of /var/lib/kyro/profiles)" "root:kyro 750"
expect "boot" "$(mode_of /var/lib/kyro/boot)" "root:kyro 750"
expect "state" "$(mode_of /var/lib/kyro/state)" "kyro:kyro 750"
if [ -d /etc/systemd ] && [ ! -f /etc/systemd/system/kyro.service ]; then fail "no unit file"; fi
[ -f /usr/local/share/doc/kyro/LICENSE ] || fail "no LICENSE"
[ -d /usr/local/share/doc/kyro/licenses ] || fail "no third-party licenses"
pass "user, directories, owners and modes"

grep -q "^interface: $IFACE\$" /etc/kyro/config.yaml || fail "interface not in the configuration"
grep -q "^address: $ADDR\$" /etc/kyro/config.yaml || fail "address not in the configuration"
grep -E -q '^  password: [0-9a-f]{32}$' /etc/kyro/config.yaml || fail "no generated password"
pass "configuration"

kyro bootfiles verify >/dev/null || fail "the boot files are not in place"
pass "iPXE and wimboot are installed and have the pinned checksums"

grep -q "Checking this machine" /tmp/install1.log || fail "kyro doctor was not run"
grep -q "systemd is not running here" /tmp/install1.log || fail "no word about systemd"
pass "kyro doctor ran; the missing systemd is reported"

# The service user must be able to read what root's CLI wrote, and to write
# the client state.
kyro profile import --example >/dev/null
kyro arm 52:54:00:aa:bb:cc --image none --profile kyro-example --confirm-wipe >/dev/null 2>&1 &&
	fail "arming with an image that does not exist was accepted"
as_kyro() {
	if command -v setpriv >/dev/null 2>&1; then
		setpriv --reuid kyro --regid kyro --clear-groups "$@"
	else
		su -s /bin/sh kyro -c "$*"
	fi
}
if command -v setpriv >/dev/null 2>&1 || command -v su >/dev/null 2>&1; then
	as_kyro kyro profile list | grep -q kyro-example || fail "the user kyro cannot read the profiles"
	as_kyro kyro clients >/dev/null || fail "the user kyro cannot read the client state"
	as_kyro sh -c 'touch /var/lib/kyro/state/.probe && rm /var/lib/kyro/state/.probe' || fail "the user kyro cannot write the client state"
	if as_kyro sh -c 'touch /var/lib/kyro/profiles/.probe' 2>/dev/null; then
		fail "the user kyro can write the profiles"
	fi
	pass "the service user reads images, profiles and boot files and writes only the state"
else
	echo "SKIP: neither setpriv nor su here; the service user is tested in the second round"
fi

# --- running it again -----------------------------------------------------

before=$(sha256sum /etc/kyro/config.yaml)
uid_before=$(id -u kyro)
sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" \
	--interface other0 --address 203.0.113.9 --bootfiles-from /bootfiles >/tmp/install2.log 2>&1 ||
	{
		cat /tmp/install2.log
		fail "the second run failed"
	}
expect "configuration after the second run" "$(sha256sum /etc/kyro/config.yaml)" "$before"
expect "user after the second run" "$(id -u kyro)" "$uid_before"
grep -q "Kept the existing" /tmp/install2.log || fail "the second run does not say that it kept the configuration"
kyro profile list | grep -q kyro-example || fail "the profile is gone after the second run"
pass "a second run changes nothing: configuration, user and data are kept"

# --- uninstalling ---------------------------------------------------------

sh "$INSTALL" --uninstall >/dev/null
[ ! -e /usr/local/bin/kyro ] || fail "uninstall left the program"
[ ! -e /etc/systemd/system/kyro.service ] || fail "uninstall left the unit"
[ ! -e /usr/local/share/doc/kyro ] || fail "uninstall left the documents"
[ -f /etc/kyro/config.yaml ] || fail "uninstall removed the configuration"
[ -d /var/lib/kyro/profiles/kyro-example ] || fail "uninstall removed data"
id kyro >/dev/null 2>&1 || fail "uninstall removed the user"
pass "--uninstall removes the program and the unit, keeps configuration, data and user"

sh "$INSTALL" --uninstall >/dev/null
pass "--uninstall twice is harmless"

sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" --bootfiles-from /bootfiles >/dev/null 2>&1 ||
	fail "installing again after --uninstall failed"
expect "configuration after reinstalling" "$(sha256sum /etc/kyro/config.yaml)" "$before"
pass "installing again finds the old configuration and data"

# --- purging --------------------------------------------------------------

sh "$INSTALL" --purge --yes >/dev/null
nothing_installed "after --purge"
[ ! -e /etc/systemd/system/kyro.service ] || fail "purge left the unit"
pass "--purge removes everything, including the user"

sh "$INSTALL" --purge --yes >/dev/null
pass "--purge twice is harmless"

# --- finding the interface, and answering questions -------------------------

if [ -n "${WITH_IP:-}" ]; then
	command -v ip >/dev/null 2>&1 || fail "WITH_IP is set but ip is not installed"
	sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" --bootfiles-from /bootfiles >/tmp/install3.log 2>&1 ||
		{
			cat /tmp/install3.log
			fail "the installation with detection failed"
		}
	grep -q "^interface: $IFACE\$" /etc/kyro/config.yaml || fail "detected the wrong interface: $(grep ^interface /etc/kyro/config.yaml)"
	grep -q "^address: $ADDR\$" /etc/kyro/config.yaml || fail "detected the wrong address: $(grep ^address /etc/kyro/config.yaml)"
	pass "interface and address are detected ($IFACE, $ADDR)"
	sh "$INSTALL" --purge --yes >/dev/null

	if command -v script >/dev/null 2>&1; then
		# Two questions: interface (take the default) and address (typed).
		printf '\n%s\n' "$ADDR" |
			script -q -e -c "sh $INSTALL --tarball $TARBALL --checksums $SUMS --bootfiles-from /bootfiles" /tmp/install4.log >/dev/null ||
			{
				cat /tmp/install4.log
				fail "the interactive installation failed"
			}
		grep -q "Interface that faces the clients" /tmp/install4.log || fail "the interface was not asked for"
		grep -q "^interface: $IFACE\$" /etc/kyro/config.yaml || fail "interactive: wrong interface"
		grep -q "^address: $ADDR\$" /etc/kyro/config.yaml || fail "interactive: wrong address"
		pass "interactive installation asks for interface and address"

		printf 'n\n' | script -q -e -c "sh $INSTALL --purge" /tmp/purge-no.log >/dev/null && fail "purge answered with no succeeded"
		[ -f /etc/kyro/config.yaml ] || fail "purge answered with no removed the configuration"
		pass "interactive --purge, answered with no, removes nothing"
		printf 'y\n' | script -q -e -c "sh $INSTALL --purge" /tmp/purge-yes.log >/dev/null || fail "purge answered with yes failed"
		nothing_installed "after interactive purge"
		pass "interactive --purge, answered with yes, removes everything"
	fi
fi

echo "== all $checks checks passed"
