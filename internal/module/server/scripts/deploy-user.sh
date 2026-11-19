# shellcheck shell=sh
# Creates the deploy user in the docker group, or adds it to the group.
# Needs passwordless sudo. Idempotent.
set -eu

getent group docker >/dev/null || sudo -n groupadd docker
if id deploy >/dev/null 2>&1; then
	if id -nG deploy | tr ' ' '\n' | grep -qx docker; then
		echo "deploy user ready"
		exit 0
	fi
	sudo -n usermod -aG docker deploy
else
	sudo -n useradd -m -s /bin/bash -G docker deploy
fi
