#!/usr/bin/env bash
# Usage: restore.sh [RFC3339-timestamp]
set -Eeuo pipefail

ts=${1:-}
# The three overrides exist for scripts/restore_test.sh; production uses the defaults,
# which match the path in /etc/litestream.yml.
data_dir=${FIRSTLINE_DATA_DIR:-/var/lib/firstline}
env_file=${FIRSTLINE_ENV_FILE:-/etc/firstline/env}
litestream_env_file=${FIRSTLINE_LITESTREAM_ENV_FILE:-/etc/firstline/litestream.env}
db=$data_dir/firstline.db

# load_env exports the KEY=VALUE lines of $1 verbatim, as systemd's EnvironmentFile
# reads them: sourcing would cut a value at & and expand $.
# shortcut: no quote or escape handling, as no value in deploy/ is quoted.
load_env() {
	local line
	while IFS= read -r line || [ -n "$line" ]; do
		case $line in '' | '#'* | ';'*) continue ;; esac
		export "${line%%=*}=${line#*=}"
	done <"$1"
}

# minus10 prints the RFC 3339 UTC time ten minutes before $1 (now when empty).
# $1 has passed the RFC 3339 check below, which GNU date reads as is; GNU date
# alone would also take free forms such as "yesterday". BSD date needs the
# offset and fraction normalized.
minus10() {
	if date --version >/dev/null 2>&1; then
		date -u -d "${1:-now} - 10 minutes" +%Y-%m-%dT%H:%M:%SZ
	elif [ -z "$1" ]; then
		date -u -v-10M +%Y-%m-%dT%H:%M:%SZ
	else
		local norm
		norm=$(printf '%s' "$1" | sed -E 's/\.[0-9]+//; s/Z$/+0000/; s/([+-][0-9]{2}):([0-9]{2})$/\1\2/')
		date -u -j -v-10M -f %Y-%m-%dT%H:%M:%S%z "$norm" +%Y-%m-%dT%H:%M:%SZ
	fi
}

# Fail before any destructive step unless the timestamp is RFC 3339 and a real time.
rfc3339='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$'
if [ -n "$ts" ] && ! [[ $ts =~ $rfc3339 ]]; then
	echo "restore.sh: timestamp $ts is not RFC 3339 (2026-11-26T10:00:00Z)" >&2
	exit 1
fi
since=$(minus10 "$ts")

echo "step 1: stop the firstline and litestream services"
systemctl stop firstline litestream
if systemctl is-active --quiet firstline; then
	echo "restore.sh: firstline is still active after stop; aborting" >&2
	exit 1
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup=$data_dir/pre-restore-$stamp
# rollback puts the moved files back and starts the services again, so a failed
# restore leaves the host as it was. Litestream stops first because it may
# already replicate the restored file. The restored files are kept aside, as a
# failed reconcile may already have written to them. With no old database (a
# new host) the services stay stopped, or serve would start on an empty one.
rollback() {
	trap - ERR
	echo "restore.sh: failed; putting $backup back" >&2
	systemctl stop litestream
	local failed=$data_dir/failed-restore-$stamp f
	mkdir -p "$failed"
	for f in "$db" "$db-wal" "$db-shm"; do
		if [ -e "$f" ]; then mv "$f" "$failed/"; fi
	done
	for f in "$backup"/*; do
		if [ -e "$f" ]; then mv "$f" "$data_dir/"; fi
	done
	if [ -e "$db" ]; then
		systemctl start litestream firstline
	else
		echo "restore.sh: no database to put back; the services stay stopped" >&2
	fi
}
echo "step 2: move the current database files to $backup"
mkdir -p "$backup"
trap rollback ERR
for f in firstline.db firstline.db-wal firstline.db-shm; do
	if [ -e "$data_dir/$f" ]; then
		mv "$data_dir/$f" "$backup/"
	fi
done

echo "step 3: restore the database with litestream"
args=(restore -config /etc/litestream.yml -integrity-check quick)
if [ -n "$ts" ]; then
	args+=(-timestamp "$ts")
fi
# The Litestream config expands ${CF_ACCOUNT_ID} and the R2 keys from this file.
load_env "$litestream_env_file"
litestream "${args[@]}" "$db"

echo "step 4: set the owner of the restored files to firstline"
chown firstline "$db"*

echo "step 5: start the litestream service"
systemctl start litestream

echo "step 6: reconcile payments completed since $since"
load_env "$env_file"
runuser -u firstline -- firstline reconcile --since "$since"

trap - ERR
echo "step 7: start the firstline service"
systemctl start firstline
