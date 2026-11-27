#!/usr/bin/env bash
# Usage: package.sh <tag> [binary]
# Writes $OUT/firstline-<tag>-linux-arm64.tar.gz (OUT defaults to dist) holding the
# firstline binary and the deploy/ files the host needs, and prints its SHA-256.
# Without a binary it builds cmd/firstline for the t4g host.
set -euo pipefail
tag=${1:?usage: package.sh <tag> [binary]}
root=$(cd "$(dirname "$0")/.." && pwd)
out=${OUT:-$root/dist}
stage=$(mktemp -d "${TMPDIR:-/tmp}/package.XXXXXX")
trap 'rm -rf "$stage"' EXIT

if [ -n "${2:-}" ]; then
	cp "$2" "$stage/firstline"
else
	(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$stage/firstline" ./cmd/firstline)
fi
chmod 0755 "$stage/firstline"
# terraform/ runs on the operator's machine, not on the host.
(cd "$root/deploy" && tar -cf - --exclude terraform .) | (cd "$stage" && tar -xf -)

mkdir -p "$out"
name=firstline-$tag-linux-arm64.tar.gz
# COPYFILE_DISABLE and --no-xattrs keep macOS metadata out of a package built locally.
(cd "$stage" && COPYFILE_DISABLE=1 tar --no-xattrs -czf "$out/$name" .)
(cd "$out" && sha256sum "$name")
