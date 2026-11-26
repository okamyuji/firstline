#!/usr/bin/env bash
# Runs restore.sh against fake commands and checks the call order against the spec steps 1-7.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/restore_test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/bin" "$tmp/data"
# Each fake logs its arguments. litestream and runuser also log the variable the
# env files must set; mv logs and then moves for real so the backup can be checked.
# FAKE_FAIL=<command> makes that command fail.
for c in systemctl litestream runuser chown mv; do
	cat >"$tmp/bin/$c" <<'FAKE'
#!/usr/bin/env bash
name=$(basename "$0")
echo "$name $*" >>"$LOG"
case $name in
litestream) echo "env CF_ACCOUNT_ID=${CF_ACCOUNT_ID:-}" >>"$LOG" ;;
runuser) echo "env FIRSTLINE_STORE=${FIRSTLINE_STORE:-} FIRSTLINE_DSN=${FIRSTLINE_DSN:-}" >>"$LOG" ;;
esac
[ "${FAKE_FAIL:-}" = "$name" ] && exit 1
# FAKE_ACTIVE makes "systemctl is-active" report an active unit.
if [ "$name" = systemctl ] && [ "${1:-}" = is-active ]; then [ -n "${FAKE_ACTIVE:-}" ] && exit 0 || exit 3; fi
if [ "$name" = litestream ]; then echo restored >"${!#}"; fi
if [ "$name" = mv ]; then exec /bin/mv "$@"; fi
exit 0
FAKE
	chmod +x "$tmp/bin/$c"
done
echo "CF_ACCOUNT_ID=not-a-real-id" >"$tmp/litestream.env"
# systemd's EnvironmentFile takes the value verbatim; a shell would cut it at & and expand $HOME.
cat >"$tmp/env" <<'ENV'
# comment
FIRSTLINE_STORE=sqlite

FIRSTLINE_DSN=postgres://u@h/db?sslmode=disable&application_name=fl$HOME
ENV

run() {
	: >"$tmp/log"
	rm -rf "$tmp/data"
	mkdir "$tmp/data"
	# NO_DB=1 is a new host with no database yet.
	if [ -z "${NO_DB:-}" ]; then
		echo original >"$tmp/data/firstline.db"
		touch "$tmp/data/firstline.db-wal" "$tmp/data/firstline.db-shm"
	fi
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
env CF_ACCOUNT_ID=not-a-real-id
chown firstline DATA/firstline.db
systemctl start litestream
runuser -u firstline -- firstline reconcile --since 2026-11-26T09:50:00Z
env FIRSTLINE_STORE=sqlite FIRSTLINE_DSN=postgres://u@h/db?sslmode=disable&application_name=fl\$HOME
systemctl start firstline"
normalize <"$tmp/log" >"$tmp/got"
echo "$want" >"$tmp/want"
diff "$tmp/want" "$tmp/got" >&2 || fail "call order differs (with timestamp)"
# The backup directory is named by the UTC time and holds the old files.
backups=$(cd "$tmp/data" && ls -d pre-restore-*)
[[ $backups =~ ^pre-restore-[0-9]{8}T[0-9]{6}Z$ ]] || fail "backup directory name $backups"
[ "$(cat "$tmp/data/$backups/firstline.db")" = original ] || fail "old database not in the backup"
[ -e "$tmp/data/$backups/firstline.db-wal" ] && [ -e "$tmp/data/$backups/firstline.db-shm" ] || fail "WAL or SHM not in the backup"

# An offset timestamp and a fraction are converted to UTC.
run 2026-11-26T19:00:00.5+09:00
grep -qx 'runuser -u firstline -- firstline reconcile --since 2026-11-26T09:50:00Z' "$tmp/log" || fail "offset timestamp not converted"

# Without a timestamp: no -timestamp flag.
run
grep -q -- '-timestamp' "$tmp/log" && fail "-timestamp passed without an argument"
grep -Eq 'reconcile --since [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$' "$tmp/log" || fail "since missing without timestamp"

# A unit that stays active stops the run before any file moves.
if FAKE_ACTIVE=1 run 2>/dev/null; then fail "ran while firstline was active"; fi
grep -q '^mv ' "$tmp/log" && fail "files moved while firstline was active"

# A timestamp that is not RFC 3339 stops the run before systemctl is called.
for ts in not-a-time yesterday 2026-11-26 10:00 '2026-11-26 10:00:00' 2026-11-26T10:00:00 2026-13-01T00:00:00Z; do
	if run "$ts" 2>/dev/null; then fail "bad timestamp $ts accepted"; fi
	[ ! -s "$tmp/log" ] || fail "commands ran for the bad timestamp $ts"
	[ "$(cat "$tmp/data/firstline.db")" = original ] || fail "database moved for the bad timestamp $ts"
done

# A failure after the move stops Litestream, keeps the restored files aside,
# puts the old files back and starts both services again.
for step in litestream chown runuser; do
	if FAKE_FAIL=$step run 2026-11-26T10:00:00Z 2>/dev/null; then fail "a failing $step was not reported"; fi
	[ "$(cat "$tmp/data/firstline.db")" = original ] || fail "old database not put back after $step failed"
	[ -e "$tmp/data/firstline.db-wal" ] && [ -e "$tmp/data/firstline.db-shm" ] || fail "WAL or SHM not put back after $step failed"
	after=$(awk -v s="$step" '$1 == s { f = 1; next } f && $1 == "systemctl"' "$tmp/log")
	[ "$after" = 'systemctl stop litestream
systemctl start litestream firstline' ] || fail "after $step failed: $after; want Litestream stopped, then both started"
	# The fake litestream fails before it writes, so only later steps leave a restored file.
	if [ "$step" != litestream ]; then
		[ "$(cat "$tmp/data"/failed-restore-*/firstline.db)" = restored ] || fail "restored database not kept after $step failed"
	fi
done

# With no old database there is nothing to go back to: the services stay stopped.
if NO_DB=1 FAKE_FAIL=litestream run 2026-11-26T10:00:00Z 2>/dev/null; then fail "a failing restore on a new host was not reported"; fi
grep -q '^systemctl start' "$tmp/log" && fail "services started with no database to put back"
[ ! -e "$tmp/data/firstline.db" ] || fail "a database appeared on a new host after the failure"
# A missing env file fails inside load_env, which the ERR trap must still see.
mv "$tmp/litestream.env" "$tmp/litestream.env.away"
if run 2026-11-26T10:00:00Z 2>/dev/null; then fail "a missing env file was not reported"; fi
mv "$tmp/litestream.env.away" "$tmp/litestream.env"
[ "$(cat "$tmp/data/firstline.db")" = original ] || fail "old database not put back after the env file was missing"
echo "restore_test: ok"
