#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

for tool in gofmt staticcheck golangci-lint gremlins gocyclo java; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "check.sh: required tool $tool not found in PATH" >&2
		exit 1
	}
done

# testcontainers-go ignores the docker CLI's current context (colima, for one), so
# the PostgreSQL and MySQL tests would skip; pass the context's endpoint instead.
if [ -z "${DOCKER_HOST:-}" ] && command -v docker >/dev/null 2>&1; then
	DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null) || DOCKER_HOST=
	[ -n "$DOCKER_HOST" ] && export DOCKER_HOST || unset DOCKER_HOST
fi

# gremlins --diff mutates only what git diff shows, which leaves out untracked files.
untracked=$(git ls-files --others --exclude-standard -- '*.go')
if [ -n "$untracked" ]; then
	echo "check.sh: untracked Go files are skipped by gremlins --diff; git add them first:" >&2
	echo "$untracked" >&2
	exit 1
fi

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

# cover_gate <profile> fails when the total statement coverage is below 80%.
cover_gate() {
	local total
	total=$(go tool cover -func="$1" | awk '/^total:/ { sub(/%/, "", $NF); print $NF }')
	echo "total coverage: ${total}%"
	awk -v t="$total" 'BEGIN { exit !(t >= 80) }' || {
		echo "coverage ${total}% is below 80%" >&2
		return 1
	}
}

echo "== go test -race"
go test -race -count=1 -coverprofile=cover.out ./...
cover_gate cover.out

echo "== gremlins"
base=${BASE:-$(git merge-base HEAD main)}
# A push that creates a branch or rewrites history gives an all-zero or unknown
# before SHA; mutate everything since the root commit then.
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	echo "gremlins: base ${base} is not a commit; using the root commit" >&2
	base=$(git rev-list --max-parents=0 HEAD | tail -n 1)
fi
# tools/doclint is its own module: root tests cannot cover it, and --diff inside it
# matches no file, so it gets a full run in the doclint step instead.
scripts/gremlins-gate.sh --diff "$base" -E '^tools/'

echo "== crap"
scripts/crap.sh cover.out

echo "== doclint"
(cd tools/doclint && go vet ./... && golangci-lint run &&
	go test -race -count=1 -coverprofile=cover.out ./... && cover_gate cover.out &&
	../../scripts/crap.sh cover.out && ../../scripts/gremlins-gate.sh && go run . ../../docs)

echo "== tlc"
bash scripts/tlc.sh
echo "check: ok"
