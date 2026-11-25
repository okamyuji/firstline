#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v java >/dev/null 2>&1; then
	echo "tlc.sh: java not found in PATH; install a JDK (CI uses actions/setup-java)" >&2
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

# TLC extracts standard modules into java.io.tmpdir, which ignores TMPDIR
# and is not writable under a sandbox. -metadir keeps TLC's states/
# directory inside the gitignored tools/tla.
java -Djava.io.tmpdir="${TMPDIR:-/tmp}" -cp "$jar" tlc2.TLC -workers auto -metadir tools/tla/states -config spec/Purchase.cfg spec/Purchase.tla
