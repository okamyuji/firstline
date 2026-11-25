#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

for tool in gofmt staticcheck golangci-lint gremlins gocyclo java; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "check.sh: required tool $tool not found in PATH" >&2
		exit 1
	}
done

# in_const <file> <line> succeeds when the line belongs to a const declaration.
# gofmt fixes the shape: a block opens with "const (" and closes with ")" at the same indent.
in_const() {
	awk -v n="$2" '
	/^[ \t]*const[ \t]*\($/ { blk = 1; match($0, /^[ \t]*/); ind = substr($0, 1, RLENGTH) }
	NR == n { if (blk || /^[ \t]*const[ \t]/) found = 1; exit }
	blk && $0 == ind ")" { blk = 0 }
	END { exit !found }
	' "$1"
}

echo "== gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "unformatted files:" >&2
	echo "$unformatted" >&2
	exit 1
fi

pkgs=$(go list ./...) || {
	echo "check.sh: go list ./... failed" >&2
	exit 1
}
if [ -z "$pkgs" ]; then
	echo "check.sh: go list ./... found no packages" >&2
	exit 1
fi

echo "== go vet"
go vet ./...
echo "== staticcheck"
staticcheck ./...
echo "== golangci-lint"
golangci-lint run
echo "== go build"
go build ./...

echo "== go test -race"
go test -race -count=1 -coverprofile=cover.out ./...
total=$(go tool cover -func=cover.out | awk '/^total:/ { sub(/%/, "", $NF); print $NF }')
echo "total coverage: ${total}%"
awk -v t="$total" 'BEGIN { exit !(t >= 80) }' || {
	echo "coverage ${total}% is below 80%" >&2
	exit 1
}

echo "== gremlins"
base=${BASE:-$(git merge-base HEAD main)}
# gremlins exits 0 even when mutants survive, so read the summary.
out=$(gremlins unleash --timeout-coefficient 20 --diff "$base")
echo "$out"
lived=$(echo "$out" | sed -n 's/.*Lived: \([0-9][0-9]*\).*/\1/p' | tail -n 1)
if [ -z "$lived" ]; then
	echo "gremlins: summary line with Lived not found" >&2
	exit 1
fi
if [ "$lived" -gt 0 ]; then
	echo "gremlins: ${lived} mutants lived" >&2
	exit 1
fi
# TIMED OUT counts as killed: the mutant broke a loop or a wait and the tests hung.
# Coverage cannot reach a const expression, so only uncovered mutants outside const declarations fail.
uncovered=$(echo "$out" | sed -n 's/^ *NOT COVERED [A-Z_]* at \(.*\):\([0-9][0-9]*\):[0-9][0-9]*$/\1 \2/p' |
	while read -r file line; do in_const "$file" "$line" || echo "$file:$line"; done | sort -u)
if [ -n "$uncovered" ]; then
	echo "gremlins: mutants not covered by any test:" >&2
	echo "$uncovered" >&2
	exit 1
fi

echo "== crap"
scripts/crap.sh cover.out

echo "== doclint"
(cd tools/doclint && go vet ./... && golangci-lint run && go test -count=1 ./... && go run . ../../docs)

echo "== tlc"
bash scripts/tlc.sh
echo "check: ok"
