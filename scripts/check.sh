#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "unformatted files:" >&2
	echo "$unformatted" >&2
	exit 1
fi

# The root module has no packages until the first Go task lands.
if [ -z "$(go list ./... 2>/dev/null)" ]; then
	echo "== go vet / staticcheck / golangci-lint / build / test / gremlins / crap: skipped (no Go packages yet)"
else
	echo "== go vet"
	go vet ./...
	echo "== staticcheck"
	staticcheck ./...
	echo "== golangci-lint"
	golangci-lint run
	echo "== go build"
	go build ./...

	echo "== go test"
	go test -count=1 -coverprofile=cover.out ./...
	total=$(go tool cover -func=cover.out | awk '/^total:/ { sub(/%/, "", $NF); print $NF }')
	echo "total coverage: ${total}%"
	awk -v t="$total" 'BEGIN { exit !(t >= 80) }' || {
		echo "coverage ${total}% is below 80%" >&2
		exit 1
	}

	echo "== gremlins"
	# gremlins exits 0 even when mutants survive, so read the Lived count.
	out=$(gremlins unleash --diff "${BASE:-HEAD~1}")
	echo "$out"
	lived=$(echo "$out" | sed -n 's/.*Lived: \([0-9][0-9]*\).*/\1/p' | tail -n 1)
	if [ "${lived:-0}" -gt 0 ]; then
		echo "gremlins: ${lived} mutants lived" >&2
		exit 1
	fi

	echo "== crap"
	scripts/crap.sh cover.out
fi

echo "== doclint"
(cd tools/doclint && go test -count=1 ./... && go run . ../../docs)

if [ -x scripts/tlc.sh ]; then
	echo "== tlc"
	scripts/tlc.sh
fi
echo "check: ok"
