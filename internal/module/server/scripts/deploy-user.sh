# shellcheck shell=sh
# shellcheck disable=SC2086
# Creates the deploy user in the docker group, or adds it to the group.
# Needs root or passwordless sudo. Idempotent.
set -eu

if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi

getent group docker >/dev/null || $SUDO groupadd docker
if id deploy >/dev/null 2>&1; then
	if id -nG deploy | tr ' ' '\n' | grep -qx docker; then
		echo "deploy user ready"
		exit 0
	fi
	$SUDO usermod -aG docker deploy
else
	$SUDO useradd -m -s /bin/bash -G docker deploy
fi
