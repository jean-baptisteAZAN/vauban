# shellcheck shell=sh
# shellcheck disable=SC2086
# Appends "<pubkey> # vauban:<project>" to the deploy user's authorized_keys.
# $1 = public key, $2 = project. Runs as deploy itself, else as deploy through
# runuser (needs root or passwordless sudo). Idempotent: an identical line is
# never added twice.
set -eu

pub=$1
project=$2
line="$pub # vauban:$project"

if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi
if [ "$(id -un)" = deploy ]; then
	as_deploy() { "$@"; }
else
	as_deploy() { $SUDO runuser -u deploy -- "$@"; }
fi

home=$(getent passwd deploy | cut -d: -f6)
if [ -z "$home" ]; then
	echo "deploy user does not exist" >&2
	exit 1
fi

as_deploy sh -s -- "$home" "$line" <<'INNER'
set -eu
umask 077
home=$1
line=$2
mkdir -p "$home/.ssh"
chmod 700 "$home/.ssh"
touch "$home/.ssh/authorized_keys"
chmod 600 "$home/.ssh/authorized_keys"
if ! grep -qxF -e "$line" "$home/.ssh/authorized_keys"; then
	printf '%s\n' "$line" >>"$home/.ssh/authorized_keys"
fi
INNER
