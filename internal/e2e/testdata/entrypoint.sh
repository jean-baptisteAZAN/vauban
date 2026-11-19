#!/bin/sh
# Installs the admin public key given in OPS_PUBKEY, then runs dockerd in the
# background and sshd in the foreground.
set -eu
install -d -m 700 -o ops -g ops /home/ops/.ssh
printf '%s\n' "$OPS_PUBKEY" >/home/ops/.ssh/authorized_keys
chown ops:ops /home/ops/.ssh/authorized_keys
chmod 600 /home/ops/.ssh/authorized_keys
dockerd --storage-driver=vfs >/var/log/dockerd.log 2>&1 &
i=0
while ! docker info >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { cat /var/log/dockerd.log; exit 1; }
	sleep 1
done
exec /usr/sbin/sshd -D -e
