# shellcheck shell=sh
# shellcheck disable=SC2086
# Installs the vauban-managed Caddy under /opt/vauban/caddy and starts it.
# $1 = base64 compose.yaml, $2 = base64 Caddyfile. Needs root or passwordless
# sudo. An existing file that differs is never overwritten (exit 7). Never
# touches any other container.
set -eu

if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi

dir=/opt/vauban/caddy
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
printf '%s' "$1" | base64 -d >"$tmp/compose.yaml"
printf '%s' "$2" | base64 -d >"$tmp/Caddyfile"

for f in compose.yaml Caddyfile; do
	if [ -e "$dir/$f" ] && ! cmp -s "$tmp/$f" "$dir/$f"; then
		echo "vauban-caddy config was changed by hand: $dir/$f differs from what vauban writes; reconcile it or remove it, vauban will not overwrite it" >&2
		exit 7
	fi
done

$SUDO install -d -m 755 "$dir"
$SUDO install -d -o deploy -g deploy -m 755 "$dir/sites"
for f in compose.yaml Caddyfile; do
	[ -e "$dir/$f" ] || $SUDO install -m 644 "$tmp/$f" "$dir/$f"
done
$SUDO docker compose -p vauban-caddy -f "$dir/compose.yaml" up -d
echo "vauban-caddy running"
