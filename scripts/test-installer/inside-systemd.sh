#!/bin/sh
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Runs inside a container in which systemd is running, started by run.sh.
# Tests that the installer starts the service and that the service works
# as the user kyro inside the sandbox of its unit.

set -eu

TARBALL=$(ls /release/kyro_*_linux_amd64.tar.gz)
SUMS=/release/SHA256SUMS
INSTALL=/release/install.sh

pass() { echo "PASS: $*"; }
fail() {
	echo "FAILED: $*" >&2
	systemctl status kyro --no-pager >&2 2>&1 || true
	journalctl -u kyro --no-pager -n 40 >&2 2>&1 || true
	exit 1
}

ADDR=$(ip -4 -o addr show dev eth0 scope global | awk '{ split($4, a, "/"); print a[1]; exit }')

sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" --bootfiles-from /bootfiles >/tmp/install.log 2>&1 ||
	{
		cat /tmp/install.log
		fail "the installation failed"
	}
grep -q "The service is enabled and started" /tmp/install.log || {
	cat /tmp/install.log
	fail "the installer did not start the service"
}
sleep 2
systemctl is-active --quiet kyro || fail "the service is not active"
systemctl is-enabled --quiet kyro || fail "the service is not enabled"
pass "the installer enabled and started the service"

pid=$(systemctl show -p MainPID --value kyro)
expect_status() { # field want
	got=$(awk -v f="$1:" '$1 == f { print $2 }' "/proc/$pid/status")
	[ "$got" = "$2" ] || fail "$1 of the service is $got, wanted $2"
}
expect_status Uid "$(id -u kyro)"
# Bit 10 is CAP_NET_BIND_SERVICE: 0x400 and nothing else.
expect_status CapEff 0000000000000400
expect_status CapPrm 0000000000000400
expect_status CapBnd 0000000000000400
expect_status NoNewPrivs 1
pass "it runs as the user kyro with CAP_NET_BIND_SERVICE and no other capability"

listening() { ss -H -ln "$1" "sport = :$2" | grep -q "$ADDR:$2\|0.0.0.0:$2\|\*:$2"; }
listening -u 67 || fail "nothing listens on UDP 67 (DHCP)"
listening -u 4011 || fail "nothing listens on UDP 4011 (proxyDHCP)"
listening -u 69 || fail "nothing listens on UDP 69 (TFTP)"
listening -t 80 || fail "nothing listens on TCP 80 (HTTP)"
listening -t 445 || fail "nothing listens on TCP 445 (SMB)"
pass "DHCP, proxyDHCP, TFTP, HTTP and SMB are listening"

curl -fsS --max-time 5 http://127.0.0.1:8080/healthz >/dev/null || fail "the status page does not answer"
curl -fsS --max-time 5 "http://$ADDR/boot.ipxe" | grep -q '^#!ipxe' || fail "no iPXE script over HTTP"
# Secure Boot is on by default: the shim comes under its own name, and what
# it will ask for, ipxe.efi, is the signed iPXE.
(cd /tmp && tftp "$ADDR" -m binary -c get ipxe-shim.efi shim.efi) || fail "TFTP transfer of the shim failed"
(cd /tmp && tftp "$ADDR" -m binary -c get ipxe.efi got.efi) || fail "TFTP transfer failed"
same() { [ "$(sha256sum "$1" | cut -d ' ' -f 1)" = "$(sha256sum "$2" | cut -d ' ' -f 1)" ]; }
same /tmp/shim.efi /var/lib/kyro/boot/ipxe-shim.efi || fail "the shim over TFTP differs from the file on disk"
same /tmp/got.efi /var/lib/kyro/boot/ipxe-signed.efi || fail "ipxe.efi over TFTP is not the signed iPXE"
pass "HTTP, TFTP and the status page answer; the shim and the signed iPXE arrive intact over TFTP"

# What a booting client does: ask what to boot. It is not armed.
curl -fsS --max-time 5 "http://$ADDR/boot?mac=52-54-00-aa-bb-01&uuid=&serial=" | grep -q 'exit' || fail "an unknown client was not sent to its disk"
kyro clients | grep -q '52:54:00:aa:bb:01 *unknown' || fail "the client was not recorded by the service"
pass "the service wrote a client record; the CLI, as root, reads it"

# The CLI, as root, and the service, as kyro, share the state directory.
kyro disarm 52:54:00:aa:bb:01 --forget >/dev/null || fail "root's CLI cannot change a record that the service wrote"
curl -fsS --max-time 5 "http://$ADDR/boot?mac=52-54-00-aa-bb-01&uuid=&serial=" >/dev/null || fail "the service cannot write after the CLI did"
kyro clients | grep -q '52:54:00:aa:bb:01 *unknown' || fail "the client was not recorded again"
pass "root's CLI and the service change the same records"

if touch_out=$(systemd-run --quiet --wait --pipe -p User=kyro -p ProtectSystem=strict -p ReadWritePaths=/var/lib/kyro/state \
	sh -c 'touch /var/lib/kyro/state/.p && rm /var/lib/kyro/state/.p && ! touch /var/lib/kyro/boot/.p 2>/dev/null && ! touch /etc/kyro/.p 2>/dev/null' 2>&1); then
	pass "with the unit's file system view, only the state directory is writable"
else
	fail "file system view: $touch_out"
fi

systemd-analyze security kyro --no-pager | tail -1

# An upgrade restarts a running service.
sh "$INSTALL" --yes --tarball "$TARBALL" --checksums "$SUMS" --bootfiles-from /bootfiles >/tmp/install2.log 2>&1 || fail "the second run failed"
grep -q "Restarted the service" /tmp/install2.log || fail "the second run did not restart the service"
sleep 1
systemctl is-active --quiet kyro || fail "the service is not active after the second run"
pass "a second run restarts the running service"

sh "$INSTALL" --uninstall >/dev/null
systemctl is-active --quiet kyro && fail "the service still runs after --uninstall"
[ ! -e /etc/systemd/system/kyro.service ] || fail "the unit is still there"
pass "--uninstall stops and removes the service"

sh "$INSTALL" --purge --yes >/dev/null
if [ -e /var/lib/kyro ] || [ -e /etc/kyro ]; then fail "purge left data"; fi
pass "--purge removes the rest"
echo "== systemd checks passed"
