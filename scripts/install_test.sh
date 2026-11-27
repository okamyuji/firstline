#!/usr/bin/env bash
# Runs deploy/firstline-install and deploy/firstline-cert-init against fake commands,
# with every host path under a temp root, and a tarball built by scripts/package.sh.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/install_test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

fail() { echo "install_test: $*" >&2; exit 1; }

secret=not-a-real-token-key-for-the-install-test
mkdir -p "$tmp/bin" "$tmp/fixtures" "$tmp/state"
echo not-a-real-binary >"$tmp/firstline"
OUT=$tmp/fixtures bash "$root/scripts/package.sh" v9.8.7 "$tmp/firstline" >/dev/null
tarball=$tmp/fixtures/firstline-v9.8.7-linux-arm64.tar.gz
[ -s "$tarball" ] || fail "package.sh did not write $tarball"
tar_sha=$(sha256sum "$tarball" | cut -d' ' -f1)
echo not-a-real-deb >"$tmp/fixtures/junk.deb"

# Each fake logs its arguments to $LOG. curl answers IMDS and SSM like AWS and copies
# downloads from $FIXTURES; it logs its -K config only as a line count, so a key on
# argv would show up in the log. FAKE_DEBS=installed makes dpkg-query report the
# pinned versions, so the debs are not downloaded.
for c in curl apt-get dpkg dpkg-query systemctl systemd-tmpfiles useradd id chown certbot amazon-cloudwatch-agent-ctl getent; do
	cat >"$tmp/bin/$c" <<'FAKE'
#!/usr/bin/env bash
name=$(basename "$0")
echo "$name $*" >>"$LOG"
case $name in
curl)
	out= url= cfg=0
	while [ $# -gt 0 ]; do
		case $1 in
		-o) out=$2; shift ;;
		-K) [ "$2" = - ] && cfg=$(wc -l | tr -d ' '); shift ;;
		-H | -X | -d | --aws-sigv4 | -w) shift ;;
		http* | file*) url=$1 ;;
		esac
		shift
	done
	[ "$cfg" = 0 ] || echo "curl config lines: $cfg" >>"$LOG"
	case $url in
	*/latest/api/token) echo not-a-real-imds-token ;;
	*/iam/security-credentials/) echo firstline-host ;;
	*/iam/security-credentials/firstline-host) echo '{"AccessKeyId":"not-a-real-access-id","SecretAccessKey":"not-a-real-secret-access","Token":"not-a-real-session"}' ;;
	*/latest/meta-data/public-ipv4) echo 192.0.2.10 ;;
	https://ssm.*) echo "{\"Parameter\":{\"Name\":\"/firstline/token-key\",\"Value\":\"$FAKE_SECRET\"}}" ;;
	*.deb) cp "$FIXTURES/junk.deb" "$out" ;;
	*) cp "$FIXTURES/$(basename "$url")" "$out" ;;
	esac
	;;
dpkg-query) [ "${FAKE_DEBS:-}" = installed ] || exit 1
	case $* in *litestream*) printf 0.5.18 ;; *) printf 1.300075.0b2061-1 ;; esac ;;
id) [ -e "$STATE/user" ] ;;
useradd) touch "$STATE/user" ;;
getent) echo "${FAKE_DNS:-198.51.100.7} STREAM tickets.example.com" ;;
certbot) [ -z "${FAKE_CERTBOT_FAIL:-}" ] ;;
esac
FAKE
	chmod +x "$tmp/bin/$c"
done

host=$tmp/host
reset_host() {
	rm -rf "$host" "$tmp/state"/*
	: >"$tmp/log"
	mkdir -p "$host/etc/firstline"
	cat >"$host/etc/firstline/install.env" <<ENV
FIRSTLINE_VERSION=v9.8.7
FIRSTLINE_SHA256=$tar_sha
FIRSTLINE_DOMAIN=tickets.example.com
FIRSTLINE_ACME_EMAIL=ops@example.com
LITESTREAM_BUCKET=not-a-real-bucket
ENV
}

# install runs firstline-install; its output goes to $tmp/out.
install() {
	LOG=$tmp/log STATE=$tmp/state FIXTURES=$tmp/fixtures FAKE_SECRET=$secret PATH="$tmp/bin:$PATH" \
		FIRSTLINE_ROOT=$host FIRSTLINE_RELEASE_URL=https://releases.invalid/dl \
		bash "$root/deploy/firstline-install" >"$tmp/out" 2>&1
}

# A deb whose hash differs from the one in the script is never installed.
reset_host
if install; then fail "install passed with a deb of the wrong hash"; fi
grep -q 'SHA-256 mismatch' "$tmp/out" || fail "no mismatch message for the deb: $(cat "$tmp/out")"
! grep -q '^dpkg -i' "$tmp/log" || fail "dpkg -i ran on a deb of the wrong hash"

export FAKE_DEBS=installed
# A tarball whose hash differs from FIRSTLINE_SHA256 is never unpacked.
reset_host
sed -i.bak "s/^FIRSTLINE_SHA256=.*/FIRSTLINE_SHA256=$(printf '0%.0s' {1..64})/" "$host/etc/firstline/install.env"
if install; then fail "install passed with a tarball of the wrong hash"; fi
grep -q 'SHA-256 mismatch' "$tmp/out" || fail "no mismatch message for the tarball: $(cat "$tmp/out")"
[ ! -e "$host/usr/local/bin/firstline" ] || fail "firstline installed from a tarball of the wrong hash"

# A version that is not vX.Y.Z is refused before anything is downloaded.
reset_host
sed -i.bak 's/^FIRSTLINE_VERSION=.*/FIRSTLINE_VERSION=v1\/..\/9.8.7/' "$host/etc/firstline/install.env"
if install; then fail "install passed with a bad version"; fi
! grep -q '^curl' "$tmp/log" || fail "curl ran before the version was checked"

# A new host without a certificate.
reset_host
install || fail "first run failed: $(cat "$tmp/out")"
for f in usr/local/bin/firstline usr/local/bin/firstline-cert-deploy usr/local/bin/firstline-cert-init \
	usr/local/sbin/firstline-install etc/systemd/system/firstline.service etc/systemd/system/litestream.service \
	etc/systemd/system/firstline-cert-init.service etc/systemd/system/firstline-cert-init.timer \
	etc/systemd/system/logrotate.timer.d/hourly.conf etc/tmpfiles.d/firstline.conf etc/logrotate.d/firstline \
	etc/litestream.yml etc/firstline/cloudwatch-agent.json; do
	[ -s "$host/$f" ] || fail "$f not installed"
done
cmp -s "$host/usr/local/bin/firstline" "$tmp/firstline" || fail "the binary is not the one in the tarball"
[ -d "$host/etc/firstline/tls" ] || fail "/etc/firstline/tls not created for the deploy hook"
grep -qxF "FIRSTLINE_TOKEN_KEY=$secret" "$host/etc/firstline/env" || fail "the secret is not in env"
grep -qxF "FIRSTLINE_BASE_URL=https://tickets.example.com" "$host/etc/firstline/env" || fail "BASE_URL not set"
grep -qxF "FIRSTLINE_ADDR=:443" "$host/etc/firstline/env" || fail "env is not based on firstline.env.example"
grep -qxF "LITESTREAM_BUCKET=not-a-real-bucket" "$host/etc/firstline/litestream.env" || fail "litestream.env lacks the bucket"
for f in env litestream.env; do
	perm=$(stat -c %a "$host/etc/firstline/$f" 2>/dev/null || stat -f %Lp "$host/etc/firstline/$f")
	[ "$perm" = 600 ] || fail "$f mode $perm; want 600"
	grep -qxF "chown firstline:firstline $host/etc/firstline/$f" <(sed 's/\.[A-Za-z0-9]*$//' "$tmp/log") || fail "$f not owned by firstline"
done
! grep -qF "$secret" "$tmp/out" "$tmp/log" || fail "the secret appeared in the output or on a command line"
! grep -qF not-a-real-secret-access "$tmp/log" || fail "the role's secret key appeared on a command line"
grep -qxF 'curl config lines: 2' "$tmp/log" || fail "the credentials did not go to curl through -K -"
grep -q -- '--aws-sigv4 aws:amz:ap-northeast-1:ssm' "$tmp/log" || fail "the SSM call is not signed for ap-northeast-1"
grep -qF -- '-d {"Name":"/firstline/token-key","WithDecryption":true}' "$tmp/log" || fail "GetParameter not called with WithDecryption"
grep -qxF "useradd --system --user-group --home-dir /var/lib/firstline --no-create-home --shell /usr/sbin/nologin firstline" "$tmp/log" || fail "user not created"
grep -qxF "systemd-tmpfiles --create $host/etc/tmpfiles.d/firstline.conf" "$tmp/log" || fail "tmpfiles not applied"
grep -qxF "apt-get install -y -q certbot logrotate" "$tmp/log" || fail "certbot and logrotate not installed"
grep -qxF "systemctl enable --now certbot.timer logrotate.timer" "$tmp/log" || fail "the renewal and log rotation timers are not on"
grep -qxF "amazon-cloudwatch-agent-ctl -a fetch-config -m ec2 -c file:$host/etc/firstline/cloudwatch-agent.json -s" "$tmp/log" || fail "the agent was not started with the config"
grep -qxF "systemctl enable --now firstline-cert-init.timer" "$tmp/log" || fail "the certificate timer is not enabled"
! grep -qE '^systemctl (restart|start|enable --now) firstline$' "$tmp/log" || fail "firstline started without a certificate"
grep -n '^systemctl' "$tmp/log" | grep -q 'restart litestream' || fail "litestream not started"

# A second run gives the same files, and keeps a value the operator changed by hand.
sed -i.bak 's/^FIRSTLINE_HOLD_RATE_PER_MIN=.*/FIRSTLINE_HOLD_RATE_PER_MIN=20/' "$host/etc/firstline/env"
rm "$host/etc/firstline/env.bak"
snapshot() { (cd "$host" && find . -type f ! -name '*.bak' -exec sha256sum {} + | LC_ALL=C sort); }
before=$(snapshot)
: >"$tmp/log"
install || fail "second run failed: $(cat "$tmp/out")"
[ "$(snapshot)" = "$before" ] || fail "the second run changed files: $(diff <(echo "$before") <(snapshot))"
! grep -q '^useradd' "$tmp/log" || fail "the second run created the user again"

# A host with a certificate: firstline is (re)started and the timer is off.
echo cert >"$host/etc/firstline/tls/fullchain.pem"
: >"$tmp/log"
install || fail "run with a certificate failed: $(cat "$tmp/out")"
grep -qxF "systemctl restart firstline" "$tmp/log" || fail "firstline not restarted"
grep -qxF "systemctl enable firstline" "$tmp/log" || fail "firstline not enabled"
grep -qxF "systemctl disable --now firstline-cert-init.timer" "$tmp/log" || fail "the certificate timer is left on"

# firstline-cert-init waits for DNS, then runs certbot and starts firstline.
cert_init() {
	LOG=$tmp/log STATE=$tmp/state PATH="$tmp/bin:$PATH" FIRSTLINE_DOMAIN=tickets.example.com \
		FIRSTLINE_ACME_EMAIL=ops@example.com bash "$root/deploy/firstline-cert-init" >"$tmp/out" 2>&1
}
: >"$tmp/log"
cert_init || fail "cert-init failed while DNS points elsewhere: $(cat "$tmp/out")"
! grep -q '^certbot' "$tmp/log" || fail "certbot ran before DNS pointed at the host"
: >"$tmp/log"
FAKE_DNS=192.0.2.10 FAKE_CERTBOT_FAIL=1 cert_init && fail "cert-init passed when certbot failed"
! grep -q '^systemctl' "$tmp/log" || fail "systemctl ran after certbot failed"
: >"$tmp/log"
FAKE_DNS=192.0.2.10 cert_init || fail "cert-init failed: $(cat "$tmp/out")"
grep -qxF "certbot certonly --standalone -d tickets.example.com -m ops@example.com --agree-tos -n --deploy-hook /usr/local/bin/firstline-cert-deploy" "$tmp/log" || fail "certbot not run as designed"
[ "$(grep '^systemctl' "$tmp/log")" = "systemctl enable --now firstline
systemctl disable --now firstline-cert-init.timer" ] || fail "cert-init did not start firstline and stop its timer"
echo "install_test: ok"
