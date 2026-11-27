#!/usr/bin/env bash
# Checks deploy/ against the values docs/design/basic-design.md states, and runs
# the certificate deploy hook against fake commands. macOS has no
# systemd-analyze, so the unit files are compared line by line.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
design=$root/docs/design/basic-design.md
deploy=$root/deploy
tmp=$(mktemp -d "${TMPDIR:-/tmp}/deploy_test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

fail() { echo "deploy_test: $*" >&2; exit 1; }

# want_line <file> <line> fails unless the file has the exact line.
want_line() { grep -qxF -- "$2" "$1" || fail "$(basename "$1") lacks: $2"; }

# Each Key=Value in backquotes in a column of the service table must be a line
# of that unit. Column 3 is firstline.service, column 4 litestream.service.
table=$(awk '/^\| 項目 \| `firstline.service` \| `litestream.service` \|$/ { f = 1; next } f && !/^\|/ { exit } f' "$design")
[ -n "$table" ] || fail "service table not found in the design"
for col in 3 4; do
	unit=$deploy/firstline.service
	[ "$col" = 4 ] && unit=$deploy/litestream.service
	# shellcheck disable=SC2016 # the backquotes are literal markdown
	cut -d'|' -f"$col" <<<"$table" | grep -o '`[A-Za-z]*=[^`]*`' | tr -d '`' >"$tmp/want"
	[ -s "$tmp/want" ] || fail "no settings for $(basename "$unit") in the design table"
	while IFS= read -r line; do want_line "$unit" "$line"; done <"$tmp/want"
done
# The table names the user and the env files without the key.
for unit in firstline litestream; do want_line "$deploy/$unit.service" "User=firstline"; done
want_line "$deploy/firstline.service" "EnvironmentFile=/etc/firstline/env"
want_line "$deploy/litestream.service" "EnvironmentFile=/etc/firstline/litestream.env"

# Litestream writes to S3 in Tokyo with the instance role: no key may sit in the config.
want_line "$deploy/litestream.yml" "      type: s3"
want_line "$deploy/litestream.yml" "      region: ap-northeast-1"
! grep -qE 'access-key-id|secret-access-key|endpoint:' "$deploy/litestream.yml" ||
	fail "litestream.yml must not hold keys or an endpoint; the instance role signs the requests"
# shellcheck disable=SC2016 # the dollar sign and the backquotes are literal text
want_line "$deploy/litestream.yml" '      bucket: ${LITESTREAM_BUCKET}'
# shellcheck disable=SC2016 # the dollar sign and the backquotes are literal text
grep -qF '`LITESTREAM_BUCKET`' "$design" || fail "the design does not name LITESTREAM_BUCKET"
# Litestream prunes its own files, so retention must stay on; S3 versioning keeps the old versions.
! grep -A2 '^retention:' "$deploy/litestream.yml" | grep -qE '^ +enabled: *false' ||
	fail "litestream.yml must keep retention enabled"
# The role and bucket rows are the spec deploy/terraform is tested against; any edit to them,
# such as another managed policy or versioning turned off, must be deliberate here too.
# shellcheck disable=SC2016 # the backquotes are Markdown, not a command
want_line "$design" '| インスタンスロール | `CloudWatchAgentServerPolicy`と`AmazonSSMManagedInstanceCore`に加え、複製先のバケットだけに`s3:GetBucketLocation`、`s3:ListBucket`、`s3:GetObject`、`s3:PutObject`、`s3:DeleteObject`を許す |'
# shellcheck disable=SC2016 # the backquotes are Markdown, not a command
want_line "$design" '| バケット | 東京のS3に置き、パブリックアクセスをすべてブロックする。バージョニングを有効にし、ライフサイクル規則で古い版を30日後に消す。版を消す権限（`s3:DeleteObjectVersion`）とバケットの設定を変える権限はロールに与えない（下の段落） |'

# tmpfiles.d: StandardOutput=append: needs the log directory before the unit starts.
want_line "$deploy/tmpfiles-firstline.conf" "d /var/lib/firstline 0700 firstline firstline -"
want_line "$deploy/tmpfiles-firstline.conf" "d /var/log/firstline 0750 firstline firstline -"
# shellcheck disable=SC2016 # the backquotes are Markdown, not a command
grep -qF '`deploy/tmpfiles-firstline.conf`' "$design" || fail "the design does not name deploy/tmpfiles-firstline.conf"

# logrotate: the settings the design lists, and the hourly timer drop-in.
want_line "$deploy/logrotate-firstline" "/var/log/firstline/app.log {"
for line in daily 'maxsize 100M' 'rotate 7' compress copytruncate; do
	want_line "$deploy/logrotate-firstline" "    $line"
done
want_line "$deploy/logrotate.timer.d/hourly.conf" "OnCalendar=hourly"

# litestream.yml and cloudwatch-agent.json are the design's code blocks verbatim.
awk '/^```yaml$/ { f = 1; next } /^```$/ && f { exit } f' "$design" >"$tmp/litestream.yml"
cmp -s "$tmp/litestream.yml" "$deploy/litestream.yml" || fail "litestream.yml differs from the design"
awk '/^```json$/ { f = 1; next } /^```$/ && f { exit } f' "$design" >"$tmp/cloudwatch-agent.json"
cmp -s "$tmp/cloudwatch-agent.json" "$deploy/cloudwatch-agent.json" || fail "cloudwatch-agent.json differs from the design"

# The cert hook with fakes: install applies the mode without -o (no firstline
# user here), mv moves for real, systemctl only logs.
mkdir "$tmp/bin" "$tmp/live" "$tmp/tls"
for c in install systemctl; do
	cat >"$tmp/bin/$c" <<'FAKE'
#!/usr/bin/env bash
echo "$(basename "$0") $*" >>"$LOG"
if [ "$(basename "$0")" = install ]; then
	mode=$2 src=$5 dst=$6
	cp "$src" "$dst" && chmod "$mode" "$dst"
fi
FAKE
	chmod +x "$tmp/bin/$c"
done
echo new-chain >"$tmp/live/fullchain.pem"
echo new-key >"$tmp/live/privkey.pem"
echo old-chain >"$tmp/tls/fullchain.pem"
LOG=$tmp/log PATH="$tmp/bin:$PATH" RENEWED_LINEAGE=$tmp/live FIRSTLINE_TLS_DIR=$tmp/tls bash "$deploy/firstline-cert-deploy"
for name in fullchain.pem privkey.pem; do
	grep -qxF "install -m 0600 -o firstline $tmp/live/$name $tmp/tls/$name.new" "$tmp/log" || fail "$name not installed 0600 for firstline"
	[ -e "$tmp/tls/$name.new" ] && fail "$name.new left behind"
	perm=$(stat -c %a "$tmp/tls/$name" 2>/dev/null || stat -f %Lp "$tmp/tls/$name")
	[ "$perm" = 600 ] || fail "$name mode $perm; want 600"
done
[ "$(cat "$tmp/tls/fullchain.pem")" = new-chain ] || fail "fullchain.pem not replaced"
[ "$(tail -n 1 "$tmp/log")" = "systemctl try-reload-or-restart firstline" ] || fail "firstline not reloaded after the files moved"
[ "$(grep -c '^systemctl ' "$tmp/log")" = 1 ] || fail "systemctl called more than once"
if LOG=$tmp/log PATH="$tmp/bin:$PATH" FIRSTLINE_TLS_DIR=$tmp/tls bash "$deploy/firstline-cert-deploy" 2>/dev/null; then
	fail "hook ran without RENEWED_LINEAGE"
fi
# The terraform mock provider fills computed values at random, so tests/stage1.tftest.hcl
# cannot see these two; check the text of the resource blocks instead.
tf=$deploy/terraform/main.tf
# block <type> <name> prints one resource block of main.tf.
block() { awk -v h="resource \"$1\" \"$2\" {" '$0 == h { f = 1 } f { print } f && /^}$/ { exit }' "$tf"; }
lc=$(block aws_s3_bucket_lifecycle_configuration backup)
[ -n "$lc" ] || fail "main.tf has no lifecycle configuration for the backup bucket"
grep -qxF '    filter {}' <<<"$lc" || fail "the lifecycle rule must use an empty filter, so it covers every object"
! grep -qE '^ +(prefix|tag|and|object_size_)' <<<"$lc" || fail "the lifecycle rule must not narrow the objects it covers"
host=$(block aws_instance host)
grep -qxF '    ignore_changes  = [ami]' <<<"$host" ||
	fail "aws_instance.host must ignore AMI changes, or a new Ubuntu image replaces the host and its database"
# A plan that replaces or destroys the host would take the SQLite database on its root
# disk with it, and a new Elastic IP would break the DNS record; terraform must refuse.
grep -qxF '    prevent_destroy = true' <<<"$host" || fail "aws_instance.host must set prevent_destroy"
grep -qxF '    prevent_destroy = true' <<<"$(block aws_eip host)" || fail "aws_eip.host must set prevent_destroy"
echo "deploy_test: ok"
