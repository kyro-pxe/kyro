#!/usr/bin/env bash
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Test harness for the ADR 0001 validation spike. See README.md next to this
# file. Everything runs in a network namespace with no route to the outside.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="${KYRO_SPIKE_DIR:-/var/lib/kyro-spike}"
ISO="${KYRO_SPIKE_ISO:-$WORK/iso/win11.iso}"
NS=kyrospike
BR=br0
IP_SIGNED=192.168.77.1 # Samba with signing required, named account only
IP_OPEN=192.168.77.2   # Samba with default signing, guest access, log share
SMB_USER=kyro
SMB_PASS=kyro-spike
OVMF_CODE="${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.secboot.fd}"
OVMF_VARS="${OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.fd}"

# Up to four virtual machines can run side by side. KYRO_SPIKE_VM selects one.
VM="${KYRO_SPIKE_VM:-0}"
VMS="0 1 2 3"
RUN="$WORK/vm$VM"
MAC="52:54:00:77:00:0$((VM + 1))"
VM_IP="192.168.77.8$VM"

# Embedded server candidates and the address each one listens on. "kyro" is
# Kyro's own server ("kyro share"), built from this repository.
declare -A CAND_IP=([gosmbserver]=192.168.77.11 [facetfs]=192.168.77.12
	[jfjallid]=192.168.77.13 [absfs]=192.168.77.14 [kyro]=192.168.77.15)

WIMBOOT_URL=https://github.com/ipxe/wimboot/releases/download/v2.9.0/wimboot
WIMBOOT_SHA256=5f067ccdc4d084d5bf77b6c853bd0f8402dfc2b4cd1b103d358993ae97fae8e3
IPXE_URL=https://github.com/ipxe/ipxe/releases/download/v2.0.0/ipxeboot.tar.gz
IPXE_SHA256=01a526d4cc791fc30362259c609d6c506cc64a7bdff51b9a5eb788354e17eee1

nsx() { ip netns exec "$NS" "$@"; }
mon() { echo "$*" | socat - "UNIX-CONNECT:$RUN/mon.sock" >/dev/null; }

die() {
	echo "spike: $*" >&2
	exit 1
}

fetch() { # url sha256 destination
	[ -f "$3" ] || curl -fsSL -o "$3" "$1"
	echo "$2  $3" | sha256sum -c --quiet - || die "checksum mismatch for $3"
}

cmd_fetch() {
	mkdir -p "$WORK/dl" "$WORK/ipxe"
	fetch "$WIMBOOT_URL" "$WIMBOOT_SHA256" "$WORK/dl/wimboot"
	fetch "$IPXE_URL" "$IPXE_SHA256" "$WORK/dl/ipxeboot.tar.gz"
	tar -C "$WORK/ipxe" --strip-components=1 -xzf "$WORK/dl/ipxeboot.tar.gz"
}

cmd_up() {
	[ -f "$ISO" ] || die "no ISO at $ISO (set KYRO_SPIKE_ISO)"
	[ -f "$WORK/dl/wimboot" ] || die "run '$0 fetch' first"
	mkdir -p "$WORK"/{mnt,run,http,inject,tftp,log}
	chmod 777 "$WORK/log"

	mountpoint -q "$WORK/mnt" || mount -o loop,ro "$ISO" "$WORK/mnt"

	# Isolated network: a bridge inside its own namespace, no uplink.
	if ! ip netns list | grep -q "^$NS\b"; then
		ip netns add "$NS"
		nsx ip link set lo up
		nsx ip link add "$BR" type bridge
		nsx ip link set "$BR" up
	fi
	local ip vm
	for ip in "$IP_SIGNED" "$IP_OPEN" "${CAND_IP[@]}"; do
		nsx ip addr add "$ip/24" dev "$BR" 2>/dev/null || true
	done
	for vm in $VMS; do
		nsx ip link show "tap$vm" >/dev/null 2>&1 && continue
		nsx ip tuntap add dev "tap$vm" mode tap
		nsx ip link set "tap$vm" master "$BR" up
	done

	# HTTP root: wimboot, the ISO tree, the injected files, the iPXE scripts.
	# boot.ipxe sends each machine to the script of its own test case.
	ln -sfn "$WORK/dl/wimboot" "$WORK/http/wimboot"
	ln -sfn "$WORK/mnt" "$WORK/http/iso"
	ln -sfn "$WORK/inject" "$WORK/http/inject"
	cp "$WORK/ipxe/x86_64/ipxe.efi" "$WORK/tftp/ipxe.efi"
	cp "$WORK/ipxe/x86_64/undionly.kpxe" "$WORK/tftp/undionly.kpxe"
	printf '#!ipxe\nchain http://%s/boot-${net0/mac:hexhyp}.ipxe\n' "$IP_SIGNED" \
		>"$WORK/http/boot.ipxe"

	if [ ! -f "$WORK/run/http.pid" ]; then
		ip netns exec "$NS" python3 -I -m http.server 80 --bind "$IP_SIGNED" --directory "$WORK/http" \
			>"$WORK/run/http.log" 2>&1 &
		echo $! >"$WORK/run/http.pid"
	fi

	# DHCP and TFTP. Firmware gets iPXE over TFTP, iPXE gets the script URL.
	[ -f "$WORK/run/dnsmasq.pid" ] && kill "$(cat "$WORK/run/dnsmasq.pid")" 2>/dev/null && sleep 1
	local hosts=()
	for vm in $VMS; do
		hosts+=("--dhcp-host=52:54:00:77:00:0$((vm + 1)),192.168.77.8$vm")
	done
	nsx dnsmasq --conf-file=/dev/null --port=0 --interface="$BR" --bind-interfaces \
		--dhcp-range=192.168.77.50,192.168.77.99,1h "${hosts[@]}" \
		--dhcp-match=set:ipxe,175 \
		--dhcp-match=set:efi64,option:client-arch,7 \
		--dhcp-match=set:efi64,option:client-arch,9 \
		--dhcp-boot=tag:!ipxe,tag:!efi64,undionly.kpxe \
		--dhcp-boot=tag:!ipxe,tag:efi64,ipxe.efi \
		--dhcp-boot=tag:ipxe,"http://$IP_SIGNED/boot.ipxe" \
		--enable-tftp --tftp-root="$WORK/tftp" \
		--dhcp-leasefile="$WORK/run/dnsmasq.leases" \
		--pid-file="$WORK/run/dnsmasq.pid" \
		--log-dhcp --log-facility="$WORK/run/dnsmasq.log"

	[ -f "$WORK/run/smbd-signed.pid" ] ||
		smbd_start signed "$IP_SIGNED" "server signing = mandatory" "map to guest = never" "smb ports = 445"
	[ -f "$WORK/run/smbd-open.pid" ] ||
		smbd_start open "$IP_OPEN" "server signing = default" "map to guest = bad user" "smb ports = 445 4445"
	echo "spike: network and services are up in namespace $NS"
}

smbd_start() { # name ip signing-line guest-line ports-line
	local name=$1 ip=$2 dir="$WORK/samba-$1"
	mkdir -p "$dir"/{private,lock,state,cache,pid,ncalrpc}
	cat >"$dir/smb.conf" <<EOF
[global]
	server role = standalone server
	netbios name = KYRO-$name
	interfaces = $ip/24
	bind interfaces only = yes
	disable netbios = yes
	$5
	server min protocol = SMB2_10
	$3
	$4
	guest account = nobody
	username map = $dir/usermap
	passdb backend = tdbsam:$dir/private/passdb.tdb
	private dir = $dir/private
	lock directory = $dir/lock
	state directory = $dir/state
	cache directory = $dir/cache
	pid directory = $dir/pid
	ncalrpc dir = $dir/ncalrpc
	log file = $dir/log.%m
	log level = 1 auth:3 smb2:3
	load printers = no
	printcap name = /dev/null
	acl allow execute always = yes

[win11]
	path = $WORK/mnt
	read only = yes
	guest ok = no
	valid users = nobody

[log]
	path = $WORK/log
	read only = no
	guest ok = no
	valid users = nobody
	force user = nobody
EOF
	if [ "$name" = open ]; then
		cat >>"$dir/smb.conf" <<EOF

[guest]
	path = $WORK/mnt
	read only = yes
	guest ok = yes
EOF
	fi
	# The share account maps to the existing unix account "nobody", so the
	# spike does not have to create a system user.
	echo "nobody = $SMB_USER" >"$dir/usermap"
	printf '%s\n%s\n' "$SMB_PASS" "$SMB_PASS" |
		smbpasswd -c "$dir/smb.conf" -s -a nobody >/dev/null 2>&1
	ip netns exec "$NS" smbd --configfile="$dir/smb.conf" --foreground --no-process-group \
		>"$dir/smbd.out" 2>&1 &
	echo $! >"$WORK/run/smbd-$name.pid"
}

cmd_cand() { # name: builds and starts one embedded server candidate
	local name=$1 ip=${CAND_IP[$1]:-}
	[ -n "$ip" ] || die "unknown candidate: $name (${!CAND_IP[*]})"
	mkdir -p "$WORK/bin"
	pkill -f "$WORK/bin/smb-$name " && sleep 1
	if [ "$name" = kyro ]; then
		(cd "$HERE/../.." && go build -o "$WORK/bin/smb-kyro" ./cmd/kyro)
		ip netns exec "$NS" "$WORK/bin/smb-kyro" share -listen "$ip:445" -root "$WORK/mnt" \
			-name win11 -user "$SMB_USER" -password "$SMB_PASS" -debug \
			>"$WORK/run/cand-$name.log" 2>&1 &
	else
		(cd "$HERE/smbcand" && go build -o "$WORK/bin/smb-$name" "./$name")
		ip netns exec "$NS" "$WORK/bin/smb-$name" -addr "$ip:445" -root "$WORK/mnt" -share win11 \
			-user "$SMB_USER" -pass "$SMB_PASS" >"$WORK/run/cand-$name.log" 2>&1 &
	fi
	echo $! >"$WORK/run/cand-$name.pid"
	echo "spike: candidate $name listens on $ip:445"
}

cmd_down() {
	local vm f
	for vm in $VMS; do
		KYRO_SPIKE_VM=$vm "$0" vm-stop || true
	done
	for f in "$WORK"/run/*.pid; do
		[ -f "$f" ] || continue
		kill "$(cat "$f")" 2>/dev/null || true
		rm -f "$f"
	done
	ip netns pids "$NS" 2>/dev/null | xargs -r kill 2>/dev/null || true
	ip netns del "$NS" 2>/dev/null || true
	mountpoint -q "$WORK/mnt" && umount "$WORK/mnt"
	echo "spike: down"
}

# cmd_case installs the injected files of one test case for the selected VM.
# KYRO_SPIKE_SERVER overrides the SMB server that the case installs from.
cmd_case() {
	local script="$HERE/cases/$1.cmd" inject="$WORK/inject/$VM" f
	[ -f "$script" ] || die "no such case: $1"
	rm -rf "$inject" "$WORK/log/vm$VM"
	mkdir -p "$inject"
	cp "$HERE/common/winpeshl.ini" "$inject/"
	if grep -q '^set SETUP=' "$script"; then
		cp "$HERE/common/start.cmd" "$inject/kyro-start.cmd"
		cp "$script" "$inject/kyro-case.cmd"
	else
		cp "$script" "$inject/kyro-start.cmd"
	fi
	if grep -q autounattend.xml "$script"; then
		cp "$HERE/common/autounattend.xml" "$inject/"
	fi
	# Fill in the VM number and the server, and give batch and ini files the
	# CRLF line endings that Windows PE expects.
	sed -i "s/@VM@/$VM/g; s/^set S=.*/set S=${KYRO_SPIKE_SERVER:-$IP_SIGNED}/" "$inject"/*
	sed -i 's/\r$//; s/$/\r/' "$inject"/*.cmd "$inject"/*.ini
	{
		echo "#!ipxe"
		echo "kernel http://$IP_SIGNED/wimboot"
		for f in "$inject"/*; do
			echo "initrd http://$IP_SIGNED/inject/$VM/$(basename "$f") $(basename "$f")"
		done
		if [ "${KYRO_SPIKE_EXPLICIT_BOOTFILES:-0}" = 1 ]; then
			echo "initrd http://$IP_SIGNED/iso/efi/boot/bootx64.efi bootmgfw.efi"
			echo "initrd http://$IP_SIGNED/iso/efi/microsoft/boot/bcd BCD"
			echo "initrd http://$IP_SIGNED/iso/boot/boot.sdi boot.sdi"
		fi
		echo "initrd http://$IP_SIGNED/iso/sources/boot.wim boot.wim"
		echo "boot"
	} >"$WORK/http/boot-${MAC//:/-}.ipxe"
	echo "spike: case $1 installed for VM $VM (server ${KYRO_SPIKE_SERVER:-$IP_SIGNED})"
}

cmd_vm_start() { # [tpm|notpm] [fresh]
	local tpm=${1:-tpm}
	[ -S "$RUN/mon.sock" ] && die "VM $VM is already running"
	mkdir -p "$RUN"
	if [ "${2:-}" = fresh ] || [ ! -f "$RUN/disk.qcow2" ]; then
		rm -f "$RUN/disk.qcow2"
		qemu-img create -q -f qcow2 "$RUN/disk.qcow2" 64G
		cp "$OVMF_VARS" "$RUN/vars.fd"
	fi
	: >"$RUN/serial.log"
	# A VM that was stopped abruptly leaves half-open SMB sessions on the
	# server. The next boot reuses the same source ports and its connection
	# attempts then hang, so drop the old sessions first.
	nsx ss -K dst "$VM_IP" >/dev/null 2>&1 || true
	local tpmargs=()
	if [ "$tpm" = tpm ]; then
		mkdir -p "$RUN/tpm"
		swtpm socket --tpm2 --tpmstate dir="$RUN/tpm" \
			--ctrl type=unixio,path="$RUN/swtpm.sock" --daemon
		tpmargs=(-chardev "socket,id=chrtpm,path=$RUN/swtpm.sock"
			-tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-crb,tpmdev=tpm0)
	fi
	# The disk boots first. It is empty at the start, so the firmware falls
	# through to the network; after Setup has copied Windows it wins.
	nsx qemu-system-x86_64 -name "kyro-spike-$VM" \
		-machine q35,accel=kvm,smm=on -cpu host -smp 4 -m "${KYRO_SPIKE_RAM:-6G}" \
		-global driver=cfi.pflash01,property=secure,value=on \
		-drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
		-drive if=pflash,format=raw,file="$RUN/vars.fd" \
		-drive file="$RUN/disk.qcow2",if=none,id=d0,format=qcow2,discard=unmap \
		-device ahci,id=ahci0 -device ide-hd,drive=d0,bus=ahci0.0,bootindex=1 \
		-netdev tap,id=n0,ifname="tap$VM",script=no,downscript=no \
		-device "${KYRO_SPIKE_NIC:-e1000e}",netdev=n0,mac="$MAC",bootindex=2 \
		"${tpmargs[@]}" \
		-device qemu-xhci -device usb-tablet \
		-serial file:"$RUN/serial.log" \
		-display none -vnc unix:"$RUN/vnc.sock" \
		-qmp unix:"$RUN/qmp.sock",server,nowait \
		-monitor unix:"$RUN/mon.sock",server,nowait \
		-pidfile "$RUN/qemu.pid" -daemonize
	echo "spike: VM $VM started ($tpm)"
}

cmd_vm_stop() {
	[ -S "$RUN/mon.sock" ] && mon quit || true
	sleep 1
	rm -f "$RUN"/{mon,qmp,vnc,swtpm}.sock "$RUN/qemu.pid"
}

cmd_shot() { # output.png
	mon "screendump $RUN/shot.ppm"
	sleep 0.5
	pnmtopng "$RUN/shot.ppm" >"${1:-$RUN/shot.png}" 2>/dev/null
}

cmd_key() { # key names as understood by the QEMU monitor, e.g. ret, alt-n
	local k
	for k in "$@"; do
		mon "sendkey $k"
		sleep 0.2
	done
}

cmd_type() { # text: typed on a US keyboard layout, followed by Enter
	local text=$1 i c k
	for ((i = 0; i < ${#text}; i++)); do
		c=${text:i:1}
		case "$c" in
		[a-z0-9]) k=$c ;;
		[A-Z]) k=shift-${c,,} ;;
		' ') k=spc ;;
		'\') k=backslash ;;
		/) k=slash ;;
		.) k=dot ;;
		,) k=comma ;;
		-) k=minus ;;
		_) k=shift-minus ;;
		=) k=equal ;;
		:) k=shift-semicolon ;;
		'*') k=shift-8 ;;
		'"') k=shift-apostrophe ;;
		'>') k=shift-dot ;;
		'%') k=shift-5 ;;
		'&') k=shift-7 ;;
		*) die "type: unsupported character '$c'" ;;
		esac
		mon "sendkey $k"
		sleep 0.05
	done
	mon "sendkey ret"
}

cmd_click() { # x y in screen pixels, optional width and height of the screen
	local w=${3:-1280} h=${4:-800}
	local ax=$(($1 * 32767 / w)) ay=$(($2 * 32767 / h))
	{
		echo '{"execute":"qmp_capabilities"}'
		echo '{"execute":"input-send-event","arguments":{"events":[{"type":"abs","data":{"axis":"x","value":'"$ax"'}},{"type":"abs","data":{"axis":"y","value":'"$ay"'}}]}}'
		sleep 0.3
		echo '{"execute":"input-send-event","arguments":{"events":[{"type":"btn","data":{"down":true,"button":"left"}}]}}'
		sleep 0.1
		echo '{"execute":"input-send-event","arguments":{"events":[{"type":"btn","data":{"down":false,"button":"left"}}]}}'
		sleep 0.3
	} | socat - "UNIX-CONNECT:$RUN/qmp.sock" >/dev/null
}

cmd=${1:-help}
shift || true
case "$cmd" in
fetch | up | down | case | cand | shot | key | click | type) "cmd_$cmd" "$@" ;;
vm-start) cmd_vm_start "$@" ;;
vm-stop) cmd_vm_stop ;;
*) die "usage: $0 fetch|up|down|case <name>|cand <name>|vm-start [tpm|notpm] [fresh]|vm-stop|shot [file]|key <key>...|click <x> <y>|type <text>" ;;
esac
