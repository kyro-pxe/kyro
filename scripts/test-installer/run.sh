#!/usr/bin/env bash
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Tests deploy/install.sh in fresh containers. See README.md next to this
# file.
#
#   run.sh                 every distribution, then the systemd test
#   run.sh plain IMAGE     one distribution, without systemd
#   run.sh systemd         the service under systemd, in a Debian container
#
# Needs Docker, Go, and a directory with wimboot and ipxeboot.tar.gz in
# KYRO_BOOTFILES (the containers do not download them).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
BOOTFILES="${KYRO_BOOTFILES:-}"
VERSION=v0.0.0-installertest
IMAGES=(debian:13 ubuntu:24.04 fedora:43)

say() { echo "installer-test: $*"; }
die() {
	echo "installer-test: FAILED: $*" >&2
	exit 1
}

prepare() {
	command -v docker >/dev/null || die "docker is not installed"
	[ -n "$BOOTFILES" ] || die "set KYRO_BOOTFILES to a directory with wimboot and ipxeboot.tar.gz"
	if [ ! -f "$BOOTFILES/wimboot" ] || [ ! -f "$BOOTFILES/ipxeboot.tar.gz" ]; then
		die "$BOOTFILES lacks wimboot or ipxeboot.tar.gz"
	fi
	say "building release $VERSION"
	# The containers must find exactly one tarball.
	find "$REPO/dist" -maxdepth 1 -name 'kyro_*.tar.gz' -delete 2>/dev/null || true
	"$REPO/scripts/release/build.sh" "$VERSION" >/dev/null
}

# A container gets the release, the boot files and the test, all read-only.
mounts() {
	echo -v "$REPO/dist:/release:ro" -v "$BOOTFILES:/bootfiles:ro" \
		-v "$HERE:/test:ro" -v "$REPO/deploy:/src:ro"
}

cmd_plain() { # image
	local image=$1
	say "--- $image, as it comes"
	# shellcheck disable=SC2046
	docker run --rm $(mounts) "$image" sh /test/inside.sh
	say "--- $image, with iproute2 and util-linux installed"
	local setup
	case "$image" in
	debian* | ubuntu*) setup="apt-get -qq update >/dev/null && apt-get -qq install -y iproute2 util-linux bsdutils >/dev/null 2>&1" ;;
	*) setup="dnf -q -y install iproute util-linux util-linux-script >/dev/null 2>&1 || dnf -q -y install iproute util-linux >/dev/null" ;;
	esac
	# shellcheck disable=SC2046
	docker run --rm $(mounts) -e WITH_IP=1 "$image" sh -c "$setup && sh /test/inside.sh"
}

# The service under systemd: started by the installer, as the user kyro,
# inside the sandbox of the unit, and answering on its ports.
cmd_systemd() {
	local name=kyro-installer-systemd image=kyro-installer-systemd:test
	say "--- systemd in a Debian container"
	docker build -q -t "$image" - >/dev/null <<'EOF'
FROM debian:13
RUN apt-get -qq update && apt-get -qq install -y systemd systemd-sysv iproute2 curl tftp-hpa >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
	docker rm -f "$name" >/dev/null 2>&1 || true
	# systemd needs to mount and to manage control groups, hence the
	# privileges. The container has its own network; nothing of the host's.
	# shellcheck disable=SC2046
	docker run -d --name "$name" --privileged --cgroupns=private \
		--tmpfs /run --tmpfs /run/lock --tmpfs /tmp $(mounts) "$image" >/dev/null
	trap 'docker rm -f kyro-installer-systemd >/dev/null 2>&1 || true' EXIT
	local tries=0
	until docker exec "$name" systemctl is-system-running 2>/dev/null | grep -E -q 'running|degraded'; do
		tries=$((tries + 1))
		[ "$tries" -lt 60 ] || die "systemd did not come up in the container"
		sleep 1
	done
	docker exec "$name" sh /test/inside-systemd.sh
	docker rm -f "$name" >/dev/null
	trap - EXIT
}

case "${1:-all}" in
all)
	prepare
	for image in "${IMAGES[@]}"; do cmd_plain "$image"; done
	cmd_systemd
	say "ALL INSTALLER TESTS PASSED"
	;;
plain)
	prepare
	cmd_plain "${2:?image}"
	;;
systemd)
	prepare
	cmd_systemd
	;;
*) die "usage: $0 [all|plain IMAGE|systemd]" ;;
esac
