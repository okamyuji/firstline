#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# TLC runs on the JVM; a pinned Temurin image keeps Java off the developer's machine.
image=eclipse-temurin:21-jre@sha256:d77e090ae1f6970c047b443c6f0a6c1358b0426d44432bb5a7753408cd43de0d
if ! command -v docker >/dev/null 2>&1; then
	echo "tlc.sh: docker not found in PATH; TLC runs in $image" >&2
	exit 1
fi

jar=tools/tla/tla2tools.jar
url=https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar
sha256=936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88

if [ ! -f "$jar" ]; then
	mkdir -p "$(dirname "$jar")"
	curl -fsSL -o "$jar.tmp" "$url"
	mv "$jar.tmp" "$jar"
fi
actual=$(shasum -a 256 "$jar" | awk '{ print $1 }')
if [ "$actual" != "$sha256" ]; then
	echo "tla2tools.jar sha256 mismatch: got $actual, want $sha256" >&2
	exit 1
fi

# -metadir keeps TLC's states/ directory inside the gitignored tools/tla;
# --user keeps those files owned by the caller.
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD":/w -w /w "$image" \
	java -Djava.io.tmpdir=/tmp -cp "$jar" tlc2.TLC -workers auto -metadir tools/tla/states -config spec/Purchase.cfg spec/Purchase.tla
