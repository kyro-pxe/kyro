#!/usr/bin/env bash
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# End-to-end test of Kyro: a virtual machine boots from the network, is
# installed with Windows 11 without anyone touching it, and reports back.
# See README.md next to this file.
#
# Everything runs in network namespaces with no route to the outside.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
WORK="${KYRO_E2E_DIR:-/var/lib/kyro-e2e}"
ISO="${KYRO_E2E_ISO:-}"
# A directory with wimboot and ipxeboot.tar.gz, to avoid downloading them.
BOOTFILES="${KYRO_E2E_BOOTFILES:-}"
INSTALL_TIMEOUT="${KYRO_E2E_TIMEOUT:-5400}" # seconds for the installation
RAM="${KYRO_E2E_RAM:-6G}"
OVMF_CODE="${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.secboot.fd}"
OVMF_VARS="${OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.fd}"

NS=kyro-e2e           # Kyro and the virtual machines
NS_DHCP=kyro-e2e-dhcp # the DHCP server that hands out addresses
BR=br0
KYRO_IP=192.168.78.1
DHCP_IP=192.168.78.2
STATUS=127.0.0.1:8080

# Two machines: number 0 is armed and installed, number 1 is never armed.
vm_mac() { echo "52:54:00:78:00:0$(($1 + 1))"; }
vm_ip() { echo "192.168.78.8$1"; }

KYRO="$WORK/bin/kyro"
export KYRO_CONFIG="$WORK/etc/config.yaml"

say() { echo "e2e: $*"; }
die() {
	echo "e2e: FAILED: $*" >&2
	exit 1
}
nsx() { ip netns exec "$NS" "$@"; }
mon() { echo "$2" | socat - "UNIX-CONNECT:$WORK/vm$1/mon.sock" >/dev/null; }

need() {
	local tool missing=()
	for tool in "$@"; do
		command -v "$tool" >/dev/null || missing+=("$tool")
	done
	[ ${#missing[@]} -eq 0 ] || die "missing programs: ${missing[*]}"
}

cmd_check() {
	[ "$(id -u)" = 0 ] || die "run as root (network namespaces, KVM)"
	need ip qemu-system-x86_64 qemu-img swtpm dnsmasq socat curl go
	[ -e /dev/kvm ] || die "/dev/kvm is missing; the test needs KVM"
	if [ ! -f "$OVMF_CODE" ] || [ ! -f "$OVMF_VARS" ]; then
		die "OVMF not found at $OVMF_CODE (set OVMF_CODE and OVMF_VARS)"
	fi
	[ -n "$ISO" ] || die "set KYRO_E2E_ISO to a Windows 11 ISO, for example the Enterprise evaluation ISO"
	[ -f "$ISO" ] || die "no ISO at $ISO"
	mkdir -p "$WORK"
	local free
	free=$(df --output=avail -BG "$WORK" | tail -1 | tr -dc 0-9)
	[ "$free" -ge 22 ] || die "$WORK has ${free} GB free; the test needs about 22 GB"
}

# cmd_up builds Kyro, prepares its data and the network, and starts it.
cmd_up() {
	cmd_check
	mkdir -p "$WORK"/{bin,etc,data,run,log}
	say "building Kyro"
	(cd "$REPO" && CGO_ENABLED=0 go build -trimpath -o "$KYRO" ./cmd/kyro)

	cat >"$KYRO_CONFIG" <<CONF
interface: $BR
address: $KYRO_IP
data_dir: $WORK/data
dhcp:
  mode: proxy
status:
  listen: $STATUS
smb:
  password: e2e-not-a-secret
install_timeout: 3h
CONF

	say "fetching iPXE and wimboot"
	if [ -n "$BOOTFILES" ]; then
		"$KYRO" bootfiles fetch --from "$BOOTFILES" >"$WORK/log/bootfiles.log"
	else
		"$KYRO" bootfiles fetch >"$WORK/log/bootfiles.log"
	fi
	"$KYRO" bootfiles verify

	if ! "$KYRO" image list | grep -q '^win11 '; then
		say "adding the ISO (linked, not copied)"
		"$KYRO" image add "$ISO" --name win11 --link
	fi
	"$KYRO" profile import --example --replace >"$WORK/log/profile-import.log"
	# Start from a clean slate: no client is known.
	rm -rf "${WORK:?}/data/state"

	# The network: one bridge that Kyro and the machines share, and a
	# second namespace for the DHCP server, as on a real network where
	# addresses come from somewhere else.
	if ! ip netns list | grep -q "^$NS\b"; then
		ip netns add "$NS"
		nsx ip link set lo up
		nsx ip link add "$BR" type bridge
		nsx ip link set "$BR" up
		nsx ip addr add "$KYRO_IP/24" dev "$BR"
		for vm in 0 1; do
			nsx ip tuntap add dev "tap$vm" mode tap
			nsx ip link set "tap$vm" master "$BR" up
		done
	fi
	if ! ip netns list | grep -q "^$NS_DHCP\b"; then
		ip netns add "$NS_DHCP"
		ip link add veth-dhcp netns "$NS_DHCP" type veth peer name veth-br netns "$NS"
		nsx ip link set veth-br master "$BR" up
		ip netns exec "$NS_DHCP" ip link set lo up
		ip netns exec "$NS_DHCP" ip link set veth-dhcp up
		ip netns exec "$NS_DHCP" ip addr add "$DHCP_IP/24" dev veth-dhcp
	fi
	# Addresses only: this server knows nothing about network boot.
	[ -f "$WORK/run/dnsmasq.pid" ] && kill "$(cat "$WORK/run/dnsmasq.pid")" 2>/dev/null && sleep 1
	: >"$WORK/log/dnsmasq.log"
	ip netns exec "$NS_DHCP" dnsmasq --conf-file=/dev/null --port=0 \
		--interface=veth-dhcp --bind-interfaces \
		--dhcp-range=192.168.78.50,192.168.78.99,1h \
		--dhcp-host="$(vm_mac 0),$(vm_ip 0)" --dhcp-host="$(vm_mac 1),$(vm_ip 1)" \
		--dhcp-leasefile="$WORK/run/dnsmasq.leases" \
		--pid-file="$WORK/run/dnsmasq.pid" \
		--log-dhcp --log-facility="$WORK/log/dnsmasq.log"

	cmd_kyro_start
}

cmd_kyro_start() {
	[ -f "$WORK/run/kyro.pid" ] && kill "$(cat "$WORK/run/kyro.pid")" 2>/dev/null && sleep 1
	say "starting kyro serve"
	nsx "$KYRO" serve >"$WORK/log/kyro.log" 2>&1 &
	echo $! >"$WORK/run/kyro.pid"
	local tries=0
	while [ "$tries" -lt 50 ]; do
		nsx curl -fsS "http://$STATUS/healthz" >/dev/null 2>&1 && return 0
		kill -0 "$(cat "$WORK/run/kyro.pid")" 2>/dev/null || break
		tries=$((tries + 1))
		sleep 0.2
	done
	cat "$WORK/log/kyro.log" >&2
	die "kyro serve did not start"
}

cmd_down() {
	local f ns
	for vm in 0 1; do cmd_vm_stop "$vm" || true; done
	for f in "${WORK:?}"/run/*.pid; do
		[ -f "$f" ] || continue
		kill "$(cat "$f")" 2>/dev/null || true
		rm -f -- "${f:?}"
	done
	for ns in "$NS" "$NS_DHCP"; do
		ip netns pids "$ns" 2>/dev/null | xargs -r kill 2>/dev/null || true
		ip netns del "$ns" 2>/dev/null || true
	done
	say "down"
}

# cmd_vm_start starts a machine with an empty disk. The network card comes
# first in the boot order and stays there, so every boot of the machine is
# a network boot: that is the situation in which a reinstall loop would
# happen.
cmd_vm_start() { # number [fresh]
	local vm=$1 run="${WORK:?}/vm$1"
	[ -S "$run/mon.sock" ] && die "machine $vm is already running"
	mkdir -p "$run/tpm"
	if [ "${2:-}" = fresh ] || [ ! -f "$run/disk.qcow2" ]; then
		rm -f -- "${run:?}/disk.qcow2"
		qemu-img create -q -f qcow2 "$run/disk.qcow2" 64G
		cp "$OVMF_VARS" "$run/vars.fd"
	fi
	swtpm socket --tpm2 --tpmstate dir="$run/tpm" \
		--ctrl type=unixio,path="$run/swtpm.sock" --daemon
	nsx qemu-system-x86_64 -name "kyro-e2e-$vm" \
		-machine q35,accel=kvm,smm=on -cpu host -smp 4 -m "$RAM" \
		-global driver=cfi.pflash01,property=secure,value=on \
		-drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
		-drive if=pflash,format=raw,file="$run/vars.fd" \
		-drive file="$run/disk.qcow2",if=none,id=d0,format=qcow2,discard=unmap \
		-device ahci,id=ahci0 -device ide-hd,drive=d0,bus=ahci0.0,bootindex=2 \
		-netdev tap,id=n0,ifname="tap$vm",script=no,downscript=no \
		-device e1000e,netdev=n0,mac="$(vm_mac "$vm")",bootindex=1 \
		-chardev "socket,id=chrtpm,path=$run/swtpm.sock" \
		-tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-crb,tpmdev=tpm0 \
		-device qemu-xhci -device usb-tablet \
		-serial file:"$run/serial.log" \
		-display none -vnc unix:"$run/vnc.sock" \
		-monitor unix:"$run/mon.sock",server,nowait \
		-pidfile "$run/qemu.pid" -daemonize
	say "machine $vm started ($(vm_mac "$vm"))"
}

cmd_vm_stop() { # number
	local run="${WORK:?}/vm$1"
	if [ -S "$run/mon.sock" ]; then
		mon "$1" quit || true
	fi
	sleep 1
	rm -f -- "${run:?}/mon.sock" "${run:?}/vnc.sock" "${run:?}/swtpm.sock" "${run:?}/qemu.pid"
}

cmd_shot() { # number file.ppm
	mon "$1" "screendump $2"
	sleep 0.5
}

# state prints the state of a machine as Kyro sees it, or "none".
state() {
	"$KYRO" clients | awk -v k="mac:$(vm_mac "$1")" '$1 == k { print $2; found = 1 } END { if (!found) print "none" }'
}

# boots prints how often a machine has asked Kyro what to boot.
boots() {
	"$KYRO" clients --json | tr -d ' \n' | grep -o "\"client\":\"mac:$(vm_mac "$1")\"[^}]*" | grep -o '"boots":[0-9]*' | cut -d: -f2
}

# count prints how many lines of Kyro's log contain a text.
count() { grep -c -- "$1" "$WORK/log/kyro.log" || true; }

# wait_for waits until a command prints the wanted text.
wait_for() { # seconds wanted description command...
	local limit=$1 want=$2 what=$3 start now got
	shift 3
	start=$(date +%s)
	while :; do
		got=$("$@" 2>/dev/null || true)
		[ "$got" = "$want" ] && return 0
		now=$(date +%s)
		if [ $((now - start)) -ge "$limit" ]; then
			die "$what: still '$got' after ${limit}s, wanted '$want'"
		fi
		sleep 5
	done
}

# expect compares two values and stops the test if they differ.
expect() { # description got want
	if [ "$2" = "$3" ]; then
		say "PASS: $1"
	else
		die "$1: got '$2', wanted '$3'"
	fi
}

# windows_since prints "yes" once the DHCP server has seen a request from
# Windows after the given line of its log. Windows names itself "MSFT 5.0".
windows_since() {
	tail -n "+$(($1 + 1))" "$WORK/log/dnsmasq.log" | grep -q 'vendor class: MSFT' && echo yes
}

cmd_run() {
	local results started t0 during lines mac1
	started=$(date +%s)
	results="$WORK/results/$(date +%Y%m%d-%H%M%S)"
	# The trap runs after this function has returned, when its local
	# variables are gone. The path is therefore written into the trap.
	# shellcheck disable=SC2064
	trap "collect '$results'; cmd_down" EXIT
	cmd_up
	mkdir -p "$results"

	say "--- 1. a machine that was never armed gets no installer"
	cmd_vm_start 1 fresh
	wait_for 180 unknown "machine 1 asks Kyro what to boot" state 1
	sleep 20
	mac1=$(vm_mac 1 | tr -d : | tr a-f A-F)
	expect "the unarmed machine was sent to its disk" "$([ "$(count "boot: local disk.*mac=$mac1")" -ge 1 ] && echo yes)" yes
	expect "nothing was handed out to it" "$(count 'installation started')" 0
	expect "its disk is still empty" "$([ "$(stat -c %s "$WORK/vm1/disk.qcow2")" -lt 2000000 ] && echo yes)" yes
	cmd_vm_stop 1

	say "--- 2. arming needs the confirmation that a disk is wiped"
	if "$KYRO" arm "$(vm_mac 0)" --image win11 --profile kyro-example >"$results/arm-refused.txt" 2>&1; then
		die "arming without --confirm-wipe was accepted"
	fi
	say "PASS: arming without --confirm-wipe is refused"
	expect "and leaves no record" "$(state 0)" none
	"$KYRO" arm "$(vm_mac 0)" --image win11 --profile kyro-example --confirm-wipe >"$results/arm.txt"
	expect "the machine is armed" "$(state 0)" armed

	say "--- 3. the armed machine installs Windows by itself"
	t0=$(date +%s)
	cmd_vm_start 0 fresh
	wait_for 300 installing "machine 0 is given the installer" state 0
	say "installation started; waiting up to $((INSTALL_TIMEOUT / 60)) minutes for the callback"
	wait_for "$INSTALL_TIMEOUT" provisioned "the installed system reports back" state 0
	say "provisioned after $((($(date +%s) - t0) / 60)) minutes"
	expect "Kyro logged the callback" "$(count 'callback: client provisioned')" 1
	expect "the installer was handed out exactly once" "$(count 'installation started')" 1
	during=$(boots 0)
	expect "Setup's restarts went through the network and were sent to the disk ($during network boots)" \
		"$([ "$during" -ge 2 ] && echo yes)" yes

	say "--- 4. another network boot does not reinstall"
	lines=$(wc -l <"$WORK/log/dnsmasq.log")
	mon 0 system_reset
	wait_for 300 "$((during + 1))" "machine 0 asks Kyro again after the reset" boots 0
	expect "it is still provisioned" "$(state 0)" provisioned
	expect "the installer was still handed out exactly once" "$(count 'installation started')" 1
	# Seeing Windows ask for its address after the reset means that the
	# machine went on from the network to its disk and started from there.
	say "waiting for Windows to come up from the disk"
	wait_for 600 yes "Windows boots from the disk after the reset" windows_since "$lines"
	say "PASS: Windows started from the disk"

	cmd_shot 0 "$results/machine0-after-reset.ppm" || true
	# The disk of a passed run explains nothing and takes about 15 GB.
	# After a failure it stays, to be looked at.
	cmd_vm_stop 0 || true
	rm -f -- "${WORK:?}/vm0/disk.qcow2" "${WORK:?}/vm1/disk.qcow2"
	say "ALL CHECKS PASSED in $((($(date +%s) - started) / 60)) minutes. Evidence is in $results"
}

# collect keeps what explains a run, passed or failed.
collect() {
	mkdir -p "$1"
	cp "$WORK"/log/*.log "$1/" 2>/dev/null || true
	"$KYRO" clients >"$1/clients.txt" 2>/dev/null || true
	"$KYRO" clients --json >"$1/clients.json" 2>/dev/null || true
	for vm in 0 1; do
		if [ -S "$WORK/vm$vm/mon.sock" ]; then
			cmd_shot "$vm" "$1/machine$vm-at-end.ppm" 2>/dev/null || true
		fi
	done
}

cmd=${1:-run}
shift || true
case "$cmd" in
run | up | down | check) "cmd_$cmd" "$@" ;;
kyro-start) cmd_kyro_start ;;
vm-start) cmd_vm_start "$@" ;;
vm-stop) cmd_vm_stop "$@" ;;
shot) cmd_shot "$@" ;;
state) state "$@" ;;
*) die "usage: $0 [run|up|down|check|kyro-start|vm-start <n> [fresh]|vm-stop <n>|shot <n> <file>|state <n>]" ;;
esac
