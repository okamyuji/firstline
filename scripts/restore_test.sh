#!/usr/bin/env bash
# Runs restore.sh against fake commands and checks the call order against the spec steps 1-7.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/restore_test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/bin" "$tmp/data"
for c in systemctl litestream runuser chown mv; do
	cat >"$tmp/bin/$c" <<'FAKE'
#!/usr/bin/env bash
echo "$(basename "$0") $*" >>"$LOG"
# FAKE_ACTIVE makes "systemctl is-active" report an active unit.
if [ "$(basename "$0")" = systemctl ] && [ "${1:-}" = is-active ] && [ -n "${FAKE_ACTIVE:-}" ]; then exit 0; fi
if [ "$(basename "$0")" = systemctl ] && [ "${1:-}" = is-active ]; then exit 3; fi
exit 0
FAKE
	chmod +x "$tmp/bin/$c"
done
echo "CF_ACCOUNT_ID=not-a-real-id" >"$tmp/litestream.env"
echo "FIRSTLINE_STORE=sqlite" >"$tmp/env"

run() {
	: >"$tmp/log"
	touch "$tmp/data/firstline.db" "$tmp/data/firstline.db-wal" "$tmp/data/firstline.db-shm"
	LOG="$tmp/log" PATH="$tmp/bin:$PATH" FIRSTLINE_DATA_DIR="$tmp/data" \
		FIRSTLINE_ENV_FILE="$tmp/env" FIRSTLINE_LITESTREAM_ENV_FILE="$tmp/litestream.env" \
		bash "$root/scripts/restore.sh" "$@" >/dev/null
}

fail() { echo "restore_test: $*" >&2; exit 1; }

normalize() { sed "s#$tmp/data#DATA#g; s#DATA/pre-restore-[0-9TZ]*#BACKUP#g"; }

want_head='systemctl stop firstline litestream
systemctl is-active --quiet firstline
mv DATA/firstline.db BACKUP/
mv DATA/firstline.db-wal BACKUP/
mv DATA/firstline.db-shm BACKUP/'

# With a timestamp: -timestamp is passed and --since is ten minutes earlier.
run 2026-11-26T10:00:00Z
want="$want_head
litestream restore -config /etc/litestream.yml -integrity-check quick -timestamp 2026-11-26T10:00:00Z DATA/firstline.db
chown firstline DATA/firstline.db DATA/firstline.db-shm DATA/firstline.db-wal
systemctl start litestream
runuser -u firstline -- firstline reconcile --since 2026-11-26T09:50:00Z
systemctl start firstline"
[ "$(normalize <"$tmp/log")" = "$want" ] || { diff <(echo "$want") <(normalize <"$tmp/log") >&2 || true; fail "call order differs (with timestamp)"; }

# An offset timestamp is converted to UTC.
run 2026-11-26T19:00:00+09:00
grep -qx 'runuser -u firstline -- firstline reconcile --since 2026-11-26T09:50:00Z' "$tmp/log" || fail "offset timestamp not converted"

# Without a timestamp: no -timestamp flag.
run
grep -q -- '-timestamp' "$tmp/log" && fail "-timestamp passed without an argument"
grep -Eq 'reconcile --since [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$' "$tmp/log" || fail "since missing without timestamp"

# A unit that stays active stops the run before any file moves.
if FAKE_ACTIVE=1 run 2>/dev/null; then fail "ran while firstline was active"; fi
grep -q '^mv ' "$tmp/log" && fail "files moved while firstline was active"

# A bad timestamp stops the run before systemctl is called.
if run not-a-time 2>/dev/null; then fail "bad timestamp accepted"; fi
[ ! -s "$tmp/log" ] || fail "commands ran for a bad timestamp"
echo "restore_test: ok"
