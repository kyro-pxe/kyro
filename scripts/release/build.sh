#!/bin/sh
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Builds the release files of Kyro into dist/:
#
#   kyro_<version>_linux_amd64.tar.gz   the program, the installer, the unit,
#                                       the licenses
#   install.sh                          the installer, tied to this version
#   SHA256SUMS                          checksums of both
#
# Usage: scripts/release/build.sh v0.1.0-alpha
#
# It builds what is in the working tree and publishes nothing.

set -eu

version=${1:-}
case "$version" in
v[0-9]*) ;;
*)
	echo "usage: $0 <version>, for example v0.1.0-alpha" >&2
	exit 2
	;;
esac
case "$version" in
*[!A-Za-z0-9.+-]*)
	echo "$0: $version contains characters that a release name must not have" >&2
	exit 2
	;;
esac

repo=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo"

dist="$repo/dist"
name="kyro_${version#v}_linux_amd64"
stage="$dist/$name"

# The time stamp of every file in the tarball: the commit's, so that two
# builds of the same commit give the same tarball.
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}

mkdir -p "$dist"
rm -rf -- "${stage:?}"
rm -f -- "${dist:?}/$name.tar.gz" "${dist:?}/install.sh" "${dist:?}/SHA256SUMS"
mkdir -p "$stage/licenses"

echo "Building kyro $version for linux/amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
	-ldflags "-s -w -X main.version=$version" -o "$stage/kyro" ./cmd/kyro

cp LICENSE NOTICE README.md "$stage/"
cp deploy/kyro.service deploy/config.example.yaml "$stage/"

# The installer learns which release it belongs to.
sed "s/^KYRO_RELEASE=\"\"\$/KYRO_RELEASE=\"$version\"/" deploy/install.sh >"$stage/install.sh"
grep -q "^KYRO_RELEASE=\"$version\"\$" "$stage/install.sh" || {
	echo "$0: could not write the version into install.sh" >&2
	exit 1
}
chmod 0755 "$stage/install.sh" "$stage/kyro"

# The licenses of the modules that are compiled into the program go with it.
GOFLAGS=-mod=mod go version -m "$stage/kyro" | awk '$1 == "dep" { print $2 }' | while read -r module; do
	dir=$(go list -m -f '{{.Dir}}' "$module")
	[ -n "$dir" ] || {
		echo "$0: the source of $module is not in the module cache; run 'go mod download'" >&2
		exit 1
	}
	target="$stage/licenses/$(echo "$module" | tr / _)"
	mkdir -p "$target"
	found=0
	for f in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/NOTICE* "$dir"/PATENTS*; do
		[ -f "$f" ] || continue
		cp "$f" "$target/"
		found=1
	done
	[ "$found" = 1 ] || {
		echo "$0: $module has no license file; it cannot be shipped like this" >&2
		exit 1
	}
	chmod -R u+w "$target"
done

# So does the license of Go itself: its standard library is in the program.
mkdir -p "$stage/licenses/go"
cp "$(go env GOROOT)/LICENSE" "$stage/licenses/go/LICENSE"

tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
	-C "$dist" -cf - "$name" | gzip -n -9 >"$dist/$name.tar.gz"
cp "$stage/install.sh" "$dist/install.sh"
rm -rf -- "${stage:?}"

(cd "$dist" && sha256sum "$name.tar.gz" install.sh >SHA256SUMS)

echo
echo "Release files in $dist:"
(cd "$dist" && ls -l "$name.tar.gz" install.sh SHA256SUMS && echo && cat SHA256SUMS)
