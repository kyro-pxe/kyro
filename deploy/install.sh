#!/bin/sh
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Installs Kyro on a Linux server from a release tarball whose SHA-256
# checksum it has verified. It never installs from a source branch.
# docs/install.md describes everything this script does.

set -eu

# The release this copy of the script belongs to. The release build fills
# it in; in the source tree it is empty and --version or --tarball is needed.
KYRO_RELEASE=""

BASE_URL_DEFAULT="https://github.com/kyro-pxe/kyro/releases/download"

BIN=/usr/local/bin/kyro
DOC_DIR=/usr/local/share/doc/kyro
ETC_DIR=/etc/kyro
CONFIG=/etc/kyro/config.yaml
DATA_DIR=/var/lib/kyro
UNIT=/etc/systemd/system/kyro.service
SVC_USER=kyro

version=${KYRO_VERSION:-$KYRO_RELEASE}
tarball=${KYRO_TARBALL:-}
checksums=${KYRO_CHECKSUMS:-}
sha256=${KYRO_SHA256:-}
base_url=${KYRO_BASE_URL:-$BASE_URL_DEFAULT}
interface=${KYRO_INTERFACE:-}
address=${KYRO_ADDRESS:-}
dhcp_mode=${KYRO_DHCP_MODE:-proxy}
secure_boot=${KYRO_SECURE_BOOT:-true}
bootfiles_from=${KYRO_BOOTFILES_FROM:-}
action=install
assume_yes=0
start=1
bootfiles=1
tmp=""

usage() {
	cat <<'EOF'
Usage: install.sh [options]
       install.sh --uninstall | --purge

Installs Kyro from a release tarball after verifying its SHA-256 checksum.
Run it as root.

Where the release comes from (one of):
  --version V          Download release V (for example v0.1.0-alpha) and its
                       SHA256SUMS from the release page. This is the default
                       for a copy of install.sh that came from a release.
  --tarball FILE       Use a tarball that is already on this machine,
                       together with --checksums or --sha256.
  --checksums FILE     The SHA256SUMS file that lists the tarball.
  --sha256 HEX         The checksum the tarball must have.

Configuration (asked for interactively if not given):
  --interface NAME     Network interface that faces the clients.
  --address IPV4       This machine's address on that interface.
  --dhcp-mode MODE     proxy (default) or off.
  --no-secure-boot     Hand UEFI clients the unsigned iPXE. Only clients with
                       Secure Boot switched off can boot it. Without this
                       option clients get the signed shim and iPXE, which
                       work with Secure Boot on and off.

Behaviour:
  -y, --yes            Ask nothing; take the given values and the defaults.
  --no-start           Install, but do not enable or start the service.
  --no-bootfiles       Do not fetch iPXE and wimboot now.
  --bootfiles-from DIR Take iPXE and wimboot from DIR; they are still checked
                       against the checksums compiled into Kyro.

Removing:
  --uninstall          Remove the program and the service. Keep
                       /etc/kyro, /var/lib/kyro and the user "kyro".
  --purge              Remove those too, including every image and profile.

  -h, --help           Show this help.

Each option has an environment variable: KYRO_VERSION, KYRO_TARBALL,
KYRO_CHECKSUMS, KYRO_SHA256, KYRO_INTERFACE, KYRO_ADDRESS, KYRO_DHCP_MODE,
KYRO_SECURE_BOOT (true or false),
KYRO_BOOTFILES_FROM. An existing /etc/kyro/config.yaml is never changed.
EOF
}

say() { printf '%s\n' "$*"; }
warn() { printf 'install.sh: %s\n' "$*" >&2; }
die() {
	warn "$*"
	exit 1
}

cleanup() {
	rm -f -- "$BIN.new" "$UNIT.new" "$CONFIG.new"
	if [ -n "$tmp" ] && [ -d "$tmp" ]; then
		rm -rf -- "${tmp:?}"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

need_value() {
	[ $# -ge 2 ] || die "$1 needs a value"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		need_value "$@"
		version=$2
		shift
		;;
	--tarball)
		need_value "$@"
		tarball=$2
		shift
		;;
	--checksums)
		need_value "$@"
		checksums=$2
		shift
		;;
	--sha256)
		need_value "$@"
		sha256=$2
		shift
		;;
	--interface)
		need_value "$@"
		interface=$2
		shift
		;;
	--address)
		need_value "$@"
		address=$2
		shift
		;;
	--no-secure-boot) secure_boot=false ;;
	--secure-boot) secure_boot=true ;; # the default; accepted for scripts that name it
	--dhcp-mode)
		need_value "$@"
		dhcp_mode=$2
		shift
		;;
	--bootfiles-from)
		need_value "$@"
		bootfiles_from=$2
		shift
		;;
	-y | --yes) assume_yes=1 ;;
	--no-start) start=0 ;;
	--no-bootfiles) bootfiles=0 ;;
	--uninstall) action=uninstall ;;
	--purge) action=purge ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option $1 (see --help)" ;;
	esac
	shift
done

case "$secure_boot" in
true | false) ;;
*) die "KYRO_SECURE_BOOT=$secure_boot: use true or false" ;;
esac
case "$dhcp_mode" in
proxy | off) ;;
full) die "--dhcp-mode full is not implemented in this version of Kyro; use proxy or off" ;;
*) die "--dhcp-mode $dhcp_mode: use proxy or off" ;;
esac

# Questions are asked only when someone can answer them.
interactive=0
if [ "$assume_yes" = 0 ] && [ -t 0 ]; then
	interactive=1
fi

# ask prints a question with a default and reads the answer into $answer.
ask() { # question default
	printf '%s [%s]: ' "$1" "$2"
	IFS= read -r answer || answer=""
	[ -n "$answer" ] || answer=$2
}

# confirm asks a yes/no question; the default is the second argument.
confirm() { # question y|n
	if [ "$2" = y ]; then
		ask "$1 (Y/n)" y
	else
		ask "$1 (y/N)" n
	fi
	case "$answer" in
	y | Y | yes | Yes | YES) return 0 ;;
	*) return 1 ;;
	esac
}

have() { command -v "$1" >/dev/null 2>&1; }

systemd_running() { [ -d /run/systemd/system ] && have systemctl; }

check_platform() {
	[ "$(id -u)" = 0 ] || die "run this script as root, for example with sudo"
	[ "$(uname -s)" = Linux ] || die "Kyro runs on Linux only; this is $(uname -s)"
}

# ----------------------------------------------------------------- removing

remove_program() {
	if systemd_running; then
		systemctl disable --now kyro.service >/dev/null 2>&1 || true
	fi
	if [ -f "$UNIT" ]; then
		rm -f -- "$UNIT"
		say "Removed $UNIT"
		if systemd_running; then
			systemctl daemon-reload || true
		fi
	fi
	if [ -f "$BIN" ]; then
		rm -f -- "$BIN"
		say "Removed $BIN"
	fi
	if [ -d "$DOC_DIR" ]; then
		rm -rf -- "$DOC_DIR"
		say "Removed $DOC_DIR"
	fi
}

do_uninstall() {
	check_platform
	remove_program
	say ""
	say "Kyro is removed. Kept: $ETC_DIR, $DATA_DIR and the user \"$SVC_USER\"."
	say "Run this script with --purge to remove those as well."
}

do_purge() {
	check_platform
	if [ "$assume_yes" = 0 ]; then
		[ "$interactive" = 1 ] || die "--purge deletes every image, profile and client record in $DATA_DIR; add --yes to confirm"
		say "This deletes $ETC_DIR and $DATA_DIR, including every image, profile"
		say "and client record, and the user \"$SVC_USER\"."
		confirm "Delete all of it?" n || die "nothing was removed"
	fi
	remove_program
	if [ -d "$ETC_DIR" ]; then
		rm -rf -- "$ETC_DIR"
		say "Removed $ETC_DIR"
	fi
	if [ -d "$DATA_DIR" ]; then
		rm -rf -- "$DATA_DIR"
		say "Removed $DATA_DIR"
	fi
	if id "$SVC_USER" >/dev/null 2>&1; then
		if have userdel; then
			userdel "$SVC_USER" >/dev/null 2>&1 || warn "could not remove the user $SVC_USER"
		elif have deluser; then
			deluser "$SVC_USER" >/dev/null 2>&1 || warn "could not remove the user $SVC_USER"
		fi
		id "$SVC_USER" >/dev/null 2>&1 || say "Removed the user $SVC_USER"
	fi
	say ""
	say "Kyro and its data are removed. ISOs that were added with --link were"
	say "never Kyro's and are still where they were."
}

# --------------------------------------------------------------- the release

fetch() { # url file
	if have curl; then
		curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$2" "$1"
	elif have wget; then
		wget --quiet --https-only --output-document="$2" "$1"
	else
		die "neither curl nor wget is installed"
	fi
}

sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		die "neither sha256sum nor shasum is installed"
	fi
}

# get_release leaves the verified, unpacked release in $release_dir.
get_release() {
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	*) die "there is no Kyro release for $(uname -m) yet; only x86-64" ;;
	esac
	have tar || die "tar is not installed"
	tmp=$(mktemp -d)

	if [ -n "$tarball" ]; then
		[ -f "$tarball" ] || die "no tarball at $tarball"
		if [ -z "$checksums" ] && [ -z "$sha256" ]; then
			die "--tarball needs --checksums FILE or --sha256 HEX; Kyro is not installed from a tarball that was not verified"
		fi
		name=$(basename -- "$tarball")
	else
		if [ -z "$version" ]; then
			die "this copy of install.sh comes from the source tree and belongs to no release; name one with --version, or use --tarball"
		fi
		case "$version" in
		v[0-9]*) ;;
		*) die "--version $version: a release is named like v0.1.0-alpha" ;;
		esac
		case "$version" in
		*[!A-Za-z0-9.+-]*) die "--version $version contains characters that no release name has" ;;
		esac
		case "$base_url" in
		https://*) ;;
		*) die "KYRO_BASE_URL must start with https://" ;;
		esac
		name="kyro_${version#v}_linux_${arch}.tar.gz"
		tarball="$tmp/$name"
		say "Downloading $name ($version)"
		fetch "$base_url/$version/$name" "$tarball" || die "could not download $base_url/$version/$name"
		if [ -z "$sha256" ] && [ -z "$checksums" ]; then
			checksums="$tmp/SHA256SUMS"
			fetch "$base_url/$version/SHA256SUMS" "$checksums" || die "could not download $base_url/$version/SHA256SUMS"
		fi
	fi

	if [ -z "$sha256" ]; then
		[ -f "$checksums" ] || die "no checksum file at $checksums"
		# Lines are "<checksum>  <name>"; a "*" before the name marks binary mode.
		sha256=$(awk -v n="$name" '{ f = $2; sub(/^\*/, "", f); if (f == n) { print $1; exit } }' "$checksums")
		[ -n "$sha256" ] || die "$checksums has no checksum for $name"
	fi
	sha256=$(printf '%s' "$sha256" | tr 'A-F' 'a-f')
	case "$sha256" in
	*[!0-9a-f]*) die "the expected checksum is not hexadecimal" ;;
	esac
	[ ${#sha256} -eq 64 ] || die "the expected checksum is not a SHA-256 checksum (64 hexadecimal digits)"

	got=$(sha256_of "$tarball")
	if [ "$got" != "$sha256" ]; then
		warn "CHECKSUM MISMATCH for $name"
		warn "  expected $sha256"
		warn "  got      $got"
		die "nothing was installed"
	fi
	say "Checksum of $name is correct."

	# Refuse an archive that would write outside its own directory.
	if tar -tzf "$tarball" | grep -E -q '^/|(^|/)\.\.(/|$)'; then
		die "$name contains paths that leave its directory; nothing was installed"
	fi
	mkdir "$tmp/unpacked"
	tar -xzf "$tarball" -C "$tmp/unpacked" --no-same-owner
	release_dir=""
	for d in "$tmp"/unpacked/*/; do
		if [ -f "${d}kyro" ]; then
			release_dir=${d%/}
		fi
	done
	[ -n "$release_dir" ] || die "$name does not contain the kyro program"
	for f in kyro kyro.service config.example.yaml LICENSE NOTICE; do
		[ -f "$release_dir/$f" ] || die "$name is incomplete: $f is missing"
	done
}

# ------------------------------------------------------------ configuration

default_interface() {
	have ip || return 0
	ip -4 route show default 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit } }'
}

address_of() {
	have ip || return 0
	ip -4 -o addr show dev "$1" scope global 2>/dev/null | awk '{ split($4, a, "/"); print a[1]; exit }'
}

choose_network() {
	guessed=0
	if [ -z "$interface" ]; then
		interface=$(default_interface)
		guessed=1
	fi
	if [ "$interactive" = 1 ]; then
		say ""
		say "Kyro answers booting clients on one network interface."
		if have ip; then
			say "Interfaces with an IPv4 address:"
			ip -4 -o addr show scope global 2>/dev/null | awk '{ printf "  %-12s %s\n", $2, $4 }'
		fi
		ask "Interface that faces the clients" "${interface:-none}"
		if [ "$answer" != "$interface" ]; then
			interface=$answer
			address=""
		fi
	fi
	if [ -z "$interface" ] || [ "$interface" = none ]; then
		die "could not find the network interface; name it with --interface"
	fi
	case "$interface" in
	*[!A-Za-z0-9_.:@-]*) die "--interface $interface is not an interface name" ;;
	esac
	[ -d "/sys/class/net/$interface" ] || die "there is no network interface $interface"

	if [ -z "$address" ]; then
		address=$(address_of "$interface")
	fi
	if [ "$interactive" = 1 ]; then
		ask "This machine's IPv4 address on $interface" "${address:-none}"
		address=$answer
	fi
	if [ -z "$address" ] || [ "$address" = none ]; then
		die "could not find an IPv4 address on $interface; name it with --address"
	fi
	case "$address" in
	*[!0-9.]* | *..* | .* | *.) die "--address $address is not an IPv4 address" ;;
	*.*.*.*.*) die "--address $address is not an IPv4 address" ;;
	*.*.*.*) ;;
	*) die "--address $address is not an IPv4 address" ;;
	esac

	if [ "$guessed" = 1 ] && [ "$interactive" = 0 ]; then
		say "Using interface $interface ($address): it carries the default route."
		say "If the clients are on another network, change $CONFIG."
	fi
}

random_password() {
	# 32 hexadecimal digits from the kernel's random number generator.
	od -An -N16 -tx1 /dev/urandom | tr -d ' \n'
}

write_config() {
	if [ -f "$CONFIG" ]; then
		say "Kept the existing $CONFIG."
		return 0
	fi
	password=$(random_password)
	[ ${#password} -eq 32 ] || die "could not generate a password for the install share"
	umask 027
	cat >"$CONFIG.new" <<EOF
# Kyro configuration, written by install.sh.
# $DOC_DIR/config.example.yaml explains every setting.
# Check a changed file with: kyro doctor

interface: $interface
address: $address
data_dir: $DATA_DIR

dhcp:
  mode: $dhcp_mode

# UEFI Secure Boot: signed shim and iPXE. See config.example.yaml.
secure_boot: $secure_boot

smb:
  # Not a secret: every client that is installed is told this password.
  password: $password
EOF
	umask 022
	chown "root:$SVC_USER" "$CONFIG.new"
	chmod 0640 "$CONFIG.new"
	mv -f "$CONFIG.new" "$CONFIG"
	say "Wrote $CONFIG (interface $interface, address $address, DHCP mode $dhcp_mode)."
}

# ---------------------------------------------------------------- installing

create_user() {
	if id "$SVC_USER" >/dev/null 2>&1; then
		return 0
	fi
	nologin=/usr/sbin/nologin
	[ -x "$nologin" ] || nologin=/sbin/nologin
	[ -x "$nologin" ] || nologin=/bin/false
	if have useradd; then
		useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home --shell "$nologin" "$SVC_USER"
	elif have adduser; then
		adduser --system --group --home "$DATA_DIR" --no-create-home --shell "$nologin" "$SVC_USER"
	else
		die "neither useradd nor adduser is installed; cannot create the user $SVC_USER"
	fi
	say "Created the system user $SVC_USER."
}

# make_dir creates a directory and sets owner and mode, also if it exists.
make_dir() { # path owner:group mode
	mkdir -p "$1"
	chown "$2" "$1"
	chmod "$3" "$1"
}

do_install() {
	check_platform
	get_release

	# The program is tried where it will live: the temporary directory may
	# be mounted so that nothing in it can be executed.
	mkdir -p "$(dirname "$BIN")"
	install -m 0755 "$release_dir/kyro" "$BIN.new"
	if ! new_version=$("$BIN.new" version 2>/dev/null); then
		rm -f -- "$BIN.new"
		die "the kyro program of this release does not run on this machine"
	fi

	was_active=0
	if systemd_running && systemctl is-active --quiet kyro.service; then
		was_active=1
	fi

	# Everything that can be wrong with the input is found before anything
	# on this machine is changed.
	if [ ! -f "$CONFIG" ]; then
		choose_network
	fi

	create_user

	# The program and its documents.
	mv -f "$BIN.new" "$BIN"
	mkdir -p "$DOC_DIR"
	for f in LICENSE NOTICE README.md config.example.yaml; do
		if [ -f "$release_dir/$f" ]; then
			install -m 0644 "$release_dir/$f" "$DOC_DIR/$f"
		fi
	done
	if [ -d "$release_dir/licenses" ]; then
		rm -rf -- "$DOC_DIR/licenses"
		cp -R "$release_dir/licenses" "$DOC_DIR/licenses"
	fi
	say "Installed $BIN ($new_version)."

	# The service may read everything and write the client state only.
	# Images, profiles and boot files are changed by the CLI, as root.
	make_dir "$ETC_DIR" "root:$SVC_USER" 0750
	make_dir "$DATA_DIR" "root:$SVC_USER" 0750
	make_dir "$DATA_DIR/images" "root:$SVC_USER" 0750
	make_dir "$DATA_DIR/profiles" "root:$SVC_USER" 0750
	make_dir "$DATA_DIR/boot" "root:$SVC_USER" 0750
	make_dir "$DATA_DIR/state" "$SVC_USER:$SVC_USER" 0750

	write_config

	if [ "$bootfiles" = 1 ]; then
		say ""
		say "Fetching iPXE and wimboot from their release pages. They are separate"
		say "programs under the GNU GPL version 2, used unchanged, and are checked"
		say "against checksums compiled into Kyro."
		if [ -n "$bootfiles_from" ]; then
			"$BIN" bootfiles fetch --config "$CONFIG" --from "$bootfiles_from" || die "the boot files could not be installed; Kyro cannot boot clients without them. Run 'kyro bootfiles fetch' once that is solved."
		else
			"$BIN" bootfiles fetch --config "$CONFIG" || die "the boot files could not be installed; Kyro cannot boot clients without them. Run 'kyro bootfiles fetch' once that is solved."
		fi
	else
		say "Skipped iPXE and wimboot. Run 'kyro bootfiles fetch' before the first boot."
	fi

	# A machine without any trace of systemd gets no unit file.
	if [ -d /etc/systemd ] || have systemctl; then
		mkdir -p "$(dirname "$UNIT")"
		install -m 0644 "$release_dir/kyro.service" "$UNIT.new"
		mv -f "$UNIT.new" "$UNIT"
		if systemd_running; then
			systemctl daemon-reload
		fi
	fi

	say ""
	say "Checking this machine (kyro doctor):"
	say ""
	doctor_ok=1
	"$BIN" doctor --config "$CONFIG" || doctor_ok=0
	say ""

	started=0
	if ! systemd_running; then
		say "systemd is not running here, so the service was not started."
		say "Start Kyro with: $BIN serve"
	elif [ "$was_active" = 1 ]; then
		systemctl restart kyro.service
		started=1
		say "Restarted the service with the new version."
	elif [ "$start" = 0 ]; then
		say "The service is installed but not started (--no-start)."
		say "Start it with: systemctl enable --now kyro"
	elif [ "$doctor_ok" = 0 ]; then
		say "The service was NOT started, because of the problems above."
		say "Solve them, check with 'kyro doctor', then: systemctl enable --now kyro"
	else
		go=1
		if [ "$interactive" = 1 ]; then
			say "Once started, Kyro answers every machine on $(awk '$1 == "interface:" { print $2 }' "$CONFIG") that boots from"
			say "the network. A machine that is not armed is sent to its own disk."
			confirm "Enable and start the service now?" y || go=0
		fi
		if [ "$go" = 1 ]; then
			systemctl enable --now kyro.service
			started=1
			say "The service is enabled and started."
		else
			say "Start it with: systemctl enable --now kyro"
		fi
	fi

	say ""
	say "Next steps:"
	say "  kyro image add /path/to/windows11.iso --name win11"
	say "  kyro profile import /path/to/autounattend.xml --name my-profile"
	say "  kyro arm <mac address> --image win11 --profile my-profile"
	say "  kyro clients"
	if [ "$started" = 1 ]; then
		say "Logs: journalctl -u kyro -f"
	fi
	say "Kyro did not change your firewall. 'kyro doctor' shows what to open."
}

case "$action" in
install) do_install ;;
uninstall) do_uninstall ;;
purge) do_purge ;;
esac
