#!/usr/bin/env bash
# Usage: scripts/crap.sh <coverprofile>
# CRAP = c*c*(1-p)^3 + c, joined per function on file:line. Functions missing from the profile have p=0.
set -euo pipefail

profile=${1:?usage: crap.sh <coverprofile>}
limit=15
mod=$(go list -m)

cov=$(go tool cover -func="$profile")
all=$(gocyclo -over 0 .)
cyc=$(printf '%s\n' "$all" | grep -v '_test\.go:' | grep -v '^[0-9]* [^ ]* [^ ]* \(\./\)\?tools/' || true)
if [ -z "$cyc" ]; then
	echo "crap.sh: gocyclo found no functions" >&2
	exit 1
fi

awk -v mod="$mod" -v limit="$limit" '
FNR == NR {
	# cover -func line: <mod>/<file>:<line>:\t<func>\t<pct>%
	if ($1 == "total:") next
	key = $1; sub(/:$/, "", key); sub("^" mod "/", "", key)
	pct = $NF; sub(/%$/, "", pct)
	p[key] = pct / 100
	next
}
{
	# gocyclo line: <c> <pkg> <func> <file>:<line>:<col>
	loc = $4; sub(/^\.\//, "", loc)
	n = split(loc, parts, ":")
	key = parts[1] ":" parts[2]
	c = $1
	cov = (key in p) ? p[key] : 0
	crap = c * c * (1 - cov) ^ 3 + c
	if (crap > limit) {
		printf "CRAP %.1f > %d: %s (complexity %d, coverage %.0f%%) %s\n", crap, limit, $3, c, cov * 100, key
		bad = 1
	}
}
END { exit bad }
' <(printf '%s\n' "$cov") <(printf '%s\n' "$cyc")
