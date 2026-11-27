#!/usr/bin/env bash
# Every derived amount in ADR-0006 and the stage diagrams must be one that
# scripts/cloud_costs.py prints, so a changed price or quantity cannot leave a
# stale figure behind. Unit prices quoted per GB or GiB are left out.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
adr=$(echo "$root"/docs/design/adr/ADR-0006-*.md)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/cloud_costs_test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

python3 -I "$root/scripts/cloud_costs.py" | grep -oE '[0-9]+\.[0-9]{2}\b' | sort -u >"$tmp/printed"
grep -oE 'あたり月?[0-9]+\.[0-9]{2}ドル' "$adr" | grep -oE '[0-9]+\.[0-9]{2}' | sort -u >"$tmp/unit" || true
{
	grep -oE '[0-9]+\.[0-9]{2}ドル' "$adr" | sed 's/ドル$//'
	# Diagram labels state money only after 月額; coordinates and specs are not money.
	grep -ohE 'value="[^"]*月額[^"]*"' "$root"/docs/design/diagrams/stage-*.drawio |
		grep -oE '(^|[^0-9.])[0-9]+\.[0-9]{2}($|[^0-9])' | grep -oE '[0-9]+\.[0-9]{2}'
} | sort -u | grep -vxF -f "$tmp/unit" >"$tmp/wanted" || true
missing=$(grep -vxF -f "$tmp/printed" "$tmp/wanted" || true)
[ -z "$missing" ] || { echo "cloud_costs_test: not printed by cloud_costs.py: $(tr '\n' ' ' <<<"$missing")" >&2; exit 1; }
echo "cloud_costs_test: ok"
