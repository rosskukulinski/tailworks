#!/bin/sh
# release-keygen.sh: the release signing key ceremony (docs/RELEASING.md, "Signing").
#
#   sh scripts/release-keygen.sh DIR      make a new Ed25519 key in DIR (DIR/tw-release,
#                                         DIR/tw-release.pub), add its public half to
#                                         internal/releasesig/allowed_signers, then --sync
#   sh scripts/release-keygen.sh --sync   copy allowed_signers' keys into the
#                                         "tw release signers" blocks of scripts/get.sh
#                                         and scripts/install.sh
#
# The private key never enters the repo: keep DIR outside it (an encrypted
# volume or a password manager), and store the key as the release
# environment's TW_RELEASE_SIGNING_KEY secret (the command is printed).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SIGNERS=$ROOT/internal/releasesig/allowed_signers

sync() {
	keys=$(grep '^tw-release ' "$SIGNERS" || true)
	for s in "$ROOT/scripts/get.sh" "$ROOT/scripts/install.sh"; do
		KEYS=$keys awk '
			/^# BEGIN tw release signers/ { print; print "SIGNERS='"'"'"; if (ENVIRON["KEYS"] != "") print ENVIRON["KEYS"]; print "'"'"'"; skip = 1; next }
			/^# END tw release signers/ { skip = 0 }
			!skip { print }' "$s" >"$s.tmp"
		mode=$(stat -f %Lp "$s" 2>/dev/null || stat -c %a "$s")
		mv "$s.tmp" "$s"
		chmod "$mode" "$s"
	done
	echo "release-keygen: $(printf '%s' "$keys" | grep -c . || true) key(s) pinned in scripts/get.sh and scripts/install.sh"
}

case ${1:-} in
--sync) sync; exit 0 ;;
"" | -*) sed -n '2,13p' "$0"; exit 2 ;;
esac
DIR=$1
case $(cd "$(dirname "$DIR")" 2>/dev/null && pwd)/ in
"$ROOT"/*) echo "release-keygen: $DIR is inside the repo; keep the private key out of it" >&2; exit 1 ;;
esac
mkdir -p "$DIR"
chmod 700 "$DIR"
KEY=$DIR/tw-release
[ ! -e "$KEY" ] || { echo "release-keygen: $KEY exists" >&2; exit 1; }
# No passphrase: release.yml signs non-interactively. Protect the secret instead.
ssh-keygen -q -t ed25519 -N '' -C "tw-release $(date -u +%Y-%m-%d)" -f "$KEY"
printf 'tw-release namespaces="tw-release" %s\n' "$(cat "$KEY.pub")" >>"$SIGNERS"
sync
echo "release-keygen: new key $(ssh-keygen -l -f "$KEY.pub")"
echo "  store it for release.yml:  gh secret set TW_RELEASE_SIGNING_KEY --env release <\"$KEY\""
echo "  then commit internal/releasesig/allowed_signers, scripts/get.sh and scripts/install.sh"
