#!/usr/bin/env bash
# Runs the end-to-end tests against the real binary. serve binds a unix socket at
# startup; where the host forbids that (a sandbox), the tests run in a container.
set -euo pipefail
cd "$(dirname "$0")/.."

probe=$(mktemp -d "${TMPDIR:-/tmp}/fl.XXXXXX")
trap 'rm -rf "$probe"' EXIT
if python3 -c 'import socket, sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' "$probe/p.sock" 2>/dev/null; then
	go test -tags e2e -count=1 ./e2e
	exit 0
fi

echo "e2e: binding a unix socket is not permitted here; running in a golang:1.27 container" >&2
command -v docker >/dev/null 2>&1 || {
	echo "e2e: docker not found; cannot run the e2e tests" >&2
	exit 1
}
start=$(date +%s)
# The repository belongs to the host user, not the container's root, so git
# refuses the VCS stamp; -buildvcs=false skips it.
docker run --rm -v "$PWD:/src" -w /src \
	-v "$(go env GOMODCACHE):/go/pkg/mod" -v firstline-e2e-gocache:/root/.cache/go-build \
	-e GOFLAGS='-mod=mod -buildvcs=false' golang:1.27 \
	go test -tags e2e -count=1 ./e2e
echo "e2e: container run took $(($(date +%s) - start))s" >&2
