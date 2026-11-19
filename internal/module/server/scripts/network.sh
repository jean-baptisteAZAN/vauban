# shellcheck shell=sh
# shellcheck disable=SC2086
# Creates the shared Docker network "vauban" when it does not exist.
# $1 = mode: "sudo" (act as deploy through runuser) or "direct" (the ssh user
# already may use docker). Idempotent.
set -eu

mode=$1
if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi
if [ "$mode" = sudo ] && [ "$(id -un)" != deploy ]; then
	as_deploy() { $SUDO runuser -u deploy -- "$@"; }
else
	as_deploy() { "$@"; }
fi

if as_deploy docker network inspect vauban >/dev/null 2>&1; then
	echo "network vauban ready"
	exit 0
fi
# create first, inspect on failure: race-safe against a concurrent create.
as_deploy docker network create vauban >/dev/null 2>&1 || as_deploy docker network inspect vauban >/dev/null
echo "network vauban created"
