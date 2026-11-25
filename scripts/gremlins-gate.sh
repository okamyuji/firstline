#!/usr/bin/env bash
# Usage: scripts/gremlins-gate.sh [--diff <base>] [gremlins args...]
# Runs gremlins in the current directory and fails on lived mutants, a missing
# summary, more timeouts than kills, or uncovered or skipped mutants on lines
# that need tests (outside const declarations; for skipped, on lines the diff adds).
set -euo pipefail

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

# added <base> <file> <line> succeeds when the diff from base adds the line.
added() {
	git diff -U0 --merge-base "$1" -- "$2" | awk -v n="$3" '
	/^@@/ { split($3, a, ","); s = substr(a[1], 2) + 0; c = (a[2] == "" ? 1 : a[2] + 0); if (n + 0 >= s && n + 0 < s + c) f = 1 }
	END { exit !f }
	'
}

# mutants <status> prints "file line" for each mutant with that status.
mutants() {
	echo "$out" | sed -n "s/^ *$1 [A-Z_]* at \(.*\):\([0-9][0-9]*\):[0-9][0-9]*\$/\1 \2/p"
}

# count <label> prints the number after "<label>: " in the summary.
count() {
	echo "$out" | sed -n "s/.*$1: \([0-9][0-9]*\).*/\1/p" | tail -n 1
}

# gremlins takes each hunk of "git diff --merge-base" as one run of added lines
# starting at the first one (internal/diff/diff.go newChanges in v0.6.0), so an
# added line after a context line in the same hunk is marked SKIPPED and never
# mutated. With no context lines every hunk is one run.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=diff.context GIT_CONFIG_VALUE_0=0

# gremlins exits 0 even when mutants survive, so read the summary.
out=$(gremlins unleash --timeout-coefficient 20 "$@")
echo "$out"
lived=$(count Lived)
killed=$(count Killed)
timedout=$(count "Timed out")
if [ -z "$lived" ] || [ -z "$killed" ] || [ -z "$timedout" ]; then
	echo "gremlins: summary with Killed, Lived and Timed out not found" >&2
	exit 1
fi
if [ "$lived" -gt 0 ]; then
	echo "gremlins: ${lived} mutants lived" >&2
	exit 1
fi
# TIMED OUT counts as killed: the mutant broke a loop or a wait and the tests hung.
# A machine too slow for the timeout also times out every mutant, which proves nothing.
if [ "$timedout" -gt "$killed" ]; then
	echo "gremlins: ${timedout} mutants timed out but only ${killed} were killed" >&2
	exit 1
fi
# Coverage cannot reach a const expression, so only mutants outside const declarations fail.
uncovered=$(mutants "NOT COVERED" | while read -r file line; do in_const "$file" "$line" || echo "$file:$line"; done | sort -u)
if [ -n "$uncovered" ]; then
	echo "gremlins: mutants not covered by any test:" >&2
	echo "$uncovered" >&2
	exit 1
fi
# SKIPPED means "outside the diff" (internal/engine/engine.go mutationStatus); one on an
# added line was never run, so it fails like an uncovered one.
if [ "${1:-}" = "--diff" ]; then
	skipped=$(mutants SKIPPED | while read -r file line; do
		if added "$2" "$file" "$line" && ! in_const "$file" "$line"; then echo "$file:$line"; fi
	done | sort -u)
	if [ -n "$skipped" ]; then
		echo "gremlins: mutants on changed lines were skipped:" >&2
		echo "$skipped" >&2
		exit 1
	fi
fi
