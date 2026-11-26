#!/usr/bin/env bash
# Usage: restore.sh [RFC3339-timestamp]
set -euo pipefail

ts=${1:-}
data_dir=${FIRSTLINE_DATA_DIR:-/var/lib/firstline}
env_file=${FIRSTLINE_ENV_FILE:-/etc/firstline/env}
litestream_env_file=${FIRSTLINE_LITESTREAM_ENV_FILE:-/etc/firstline/litestream.env}
db=$data_dir/firstline.db

# minus10 prints the RFC 3339 UTC time ten minutes before $1 (now when empty).
# GNU date parses RFC 3339 itself; BSD date needs the offset and fraction normalized.
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

# Fail before any destructive step when the timestamp cannot be parsed.
since=$(minus10 "$ts")

echo "step 1: stop the firstline and litestream services"
systemctl stop firstline litestream
if systemctl is-active --quiet firstline; then
	echo "restore.sh: firstline is still active after stop; aborting" >&2
	exit 1
fi

backup=$data_dir/pre-restore-$(date -u +%Y%m%dT%H%M%SZ)
echo "step 2: move the current database files to $backup"
mkdir -p "$backup"
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
set -a
# shellcheck disable=SC1090
. "$litestream_env_file"
set +a
litestream "${args[@]}" "$db"

echo "step 4: set the owner of the restored files to firstline"
chown firstline "$db"*

echo "step 5: start the litestream service"
systemctl start litestream

echo "step 6: reconcile payments completed since $since"
set -a
# shellcheck disable=SC1090
. "$env_file"
set +a
runuser -u firstline -- firstline reconcile --since "$since"

echo "step 7: start the firstline service"
systemctl start firstline
