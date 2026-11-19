# shellcheck shell=sh
# shellcheck disable=SC2086
# Manages <sites dir>/<project>.caddy and reloads the shared Caddy.
# $1 = action (write|remove|validate|reload), $2 = mode ("sudo" or "direct"),
# $3 = sites dir (absolute, one line), $4 = project, $5 = repo slug,
# $6 = caddy container, $7 = base64 site file (write only).
# write refuses unless ~deploy/apps/<project>/.vauban-owner equals the slug
# (exit 3), refuses a symlinked site file (exit 4), writes a temp file in the
# sites dir then renames it. validate/reload run caddy inside its container
# and never restart or recreate it.
set -eu

action=$1
mode=$2
sites=$3
project=$4
slug=$5
container=$6
b64=${7:-}

case "$sites" in
/*) ;;
*)
	echo "sites dir must be absolute" >&2
	exit 2
	;;
esac
if [ "$(printf '%s' "$sites" | wc -l)" != 0 ]; then
	echo "sites dir contains a newline" >&2
	exit 2
fi
case "$project" in
'' | */* | .*)
	echo "invalid project name" >&2
	exit 2
	;;
esac

if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi
if [ "$mode" = sudo ] && [ "$(id -un)" != deploy ]; then
	as_deploy() { $SUDO runuser -u deploy -- "$@"; }
else
	as_deploy() { "$@"; }
fi
# pick_writer: deploy when it can write the sites dir, else root (sudo mode only).
pick_writer() {
	if as_deploy test -w "$sites"; then
		writer() { as_deploy "$@"; }
	elif [ "$mode" = sudo ]; then
		writer() { $SUDO "$@"; }
	else
		echo "$sites is not writable by the ssh user; give it write access or run with sudo" >&2
		exit 5
	fi
}

file="$sites/$project.caddy"

case "$action" in
write)
	pick_writer
	home=$(getent passwd deploy | cut -d: -f6)
	cur=$(as_deploy head -n 1 "$home/apps/$project/.vauban-owner" 2>/dev/null || true)
	if [ "$cur" != "$slug" ]; then
		echo "VAUBAN_OWNER_CONFLICT ${cur:--}"
		exit 3
	fi
	if [ -L "$file" ]; then
		echo "$file is a symlink; refusing" >&2
		exit 4
	fi
	tmp=$(writer mktemp "$sites/.vauban-$project.XXXXXX")
	printf '%s' "$b64" | base64 -d | writer tee "$tmp" >/dev/null
	writer chmod 644 "$tmp"
	writer mv -f "$tmp" "$file"
	echo "site written"
	;;
remove)
	pick_writer
	writer rm -f "$file"
	echo "site removed"
	;;
validate)
	if ! out=$(as_deploy docker exec "$container" caddy validate --config /etc/caddy/Caddyfile 2>&1); then
		printf '%s\n' "$out" | tail -n 20 >&2
		exit 6
	fi
	;;
reload)
	as_deploy docker exec "$container" caddy reload --config /etc/caddy/Caddyfile
	;;
*)
	echo "unknown action" >&2
	exit 2
	;;
esac
