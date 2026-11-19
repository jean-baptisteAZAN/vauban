# shellcheck shell=sh
# shellcheck disable=SC2086
# Removes one project from a shared server; never touches Docker, Caddy, the
# network, the deploy user or other projects.
# $1 = action (plan|run), $2 = mode ("sudo" or "direct"), $3 = sites dir
# (absolute, or "-" when unknown), $4 = project, $5 = repo slug, $6 = caddy
# container ("-" when unknown), $7 = purge ("yes"|"no").
# plan is read-only: it checks the owner marker and, with purge, prints one
# "VAUBAN_VOLUME <name>" line per compose volume. run does the removal, in
# this order: compose down (-v with purge), the site file (only a file vauban
# recorded, then validate and reload; a failed validate restores it), the
# project dir, and last the "# vauban:<project>" authorized_keys line.
# compose interpolates ${IMAGE_TAG:?} and ${APP_PORT:?} even for down and
# config, so both calls get placeholder values.
# With the project dir absent, plan prints VAUBAN_PROJECT_ABSENT; run then
# only removes the authorized_keys line (a rerun after a crash must finish).
# stdout markers: VAUBAN_PROJECT_ABSENT (nothing else to do, exit 0),
# VAUBAN_OWNER_CONFLICT <other> (exit 3), VAUBAN_SITE_FOREIGN (file kept,
# exit 0). Exit 4 symlink, 5 cannot act as deploy / sites dir not writable, 6 caddy validate failed
# (site restored), 7 caddy reload failed.
set -eu

action=$1
mode=$2
sites=$3
project=$4
slug=$5
container=$6
purge=${7:-no}

case "$project" in
'' | */* | .* | *[!A-Za-z0-9_-]*)
	echo "invalid project name" >&2
	exit 2
	;;
esac
case "$slug" in
'' | *[!A-Za-z0-9._/-]*)
	echo "invalid repository slug" >&2
	exit 2
	;;
esac
case "$sites" in
- | /*) ;;
*)
	echo "sites dir must be absolute" >&2
	exit 2
	;;
esac
if [ "$(printf '%s' "$sites" | wc -l)" != 0 ]; then
	echo "sites dir contains a newline" >&2
	exit 2
fi
case "$container" in
*[!A-Za-z0-9_.-]*)
	echo "invalid caddy container name" >&2
	exit 2
	;;
esac

if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi
if [ "$mode" = sudo ] && [ "$(id -un)" != deploy ]; then
	as_deploy() { $SUDO runuser -u deploy -- "$@"; }
else
	as_deploy() { "$@"; }
fi

# a missing sudo must never look like an absent project.
as_deploy true || {
	echo "cannot act as deploy (sudo -n runuser failed)" >&2
	exit 5
}

home=$(getent passwd deploy | cut -d: -f6)
absent=no
if [ -z "$home" ]; then
	absent=yes
	home=""
fi
apps="$home/apps"
dir="$apps/$project"
marker="$dir/.vauban-owner"
compose="$dir/compose.yaml"
cproject="vauban-$project"

lc() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
# dc runs compose for this project with placeholders for the required vars.
dc() { as_deploy env IMAGE_TAG=destroy APP_PORT=1 docker compose -p "$cproject" --project-directory "$dir" -f "$compose" "$@"; }

# remove_key_line drops this project's authorized_keys line (exact suffix).
remove_key_line() {
	[ -n "$home" ] || return 0
	as_deploy sh -s -- "$home" "$project" <<'INNER'
set -eu
umask 077
home=$1
project=$2
ak="$home/.ssh/authorized_keys"
if [ ! -f "$ak" ]; then
	exit 0
fi
tmp=$(mktemp "$home/.ssh/.vauban-ak.XXXXXX")
trap 'rm -f "$tmp"' EXIT INT TERM
awk -v s=" # vauban:$project" '{ n = length($0) - length(s) + 1; if (n >= 1 && substr($0, n) == s) next; print }' "$ak" >"$tmp"
chmod 600 "$tmp"
if cmp -s "$tmp" "$ak"; then
	exit 0
fi
mv -f "$tmp" "$ak"
INNER
}

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

if [ "$absent" = no ]; then
	if as_deploy test -L "$apps" || as_deploy test -L "$dir"; then
		echo "$dir is a symlink; refusing" >&2
		exit 4
	fi
	as_deploy test -d "$dir" || absent=yes
fi
if [ "$absent" = yes ]; then
	if [ "$action" = run ]; then remove_key_line; fi
	echo "VAUBAN_PROJECT_ABSENT"
	exit 0
fi
if as_deploy test -L "$marker"; then
	echo "$marker is a symlink; refusing" >&2
	exit 4
fi
cur=$(as_deploy head -n 1 "$marker" 2>/dev/null || true)
if [ -z "$cur" ]; then
	echo "VAUBAN_OWNER_CONFLICT -"
	exit 3
fi
if [ "$(lc "$cur")" != "$(lc "$slug")" ]; then
	echo "VAUBAN_OWNER_CONFLICT $cur"
	exit 3
fi

file=""
if [ "$sites" != - ] && [ "$container" != - ]; then
	file="$sites/$project.caddy"
fi

if [ "$action" = plan ]; then
	# fail before anything is stopped: a site file we cannot remove.
	if [ -n "$file" ] && { as_deploy test -e "$file" || as_deploy test -L "$file"; }; then
		pick_writer
	fi
	if [ "$purge" = yes ] && as_deploy test -f "$compose"; then
		if ! vols=$(dc config --volumes 2>/dev/null); then
			echo "VAUBAN_VOLUMES_UNKNOWN"
			exit 0
		fi
		for v in $vols; do
			echo "VAUBAN_VOLUME ${cproject}_$v"
		done
	fi
	exit 0
fi
if [ "$action" != run ]; then
	echo "unknown action" >&2
	exit 2
fi

# 1. containers (and volumes with purge) of this project only.
if as_deploy test -f "$compose"; then
	if [ "$purge" = yes ]; then
		dc down -v
	else
		dc down
	fi
else
	echo "no compose.yaml: nothing to bring down"
fi

# 2. the site file, only when vauban recorded it.
reload=no
if [ -n "$file" ]; then
	bak="$sites/.vauban-$project.bak"
	hold="$sites/.vauban-$project.destroy"
	pick_writer
	if as_deploy test -e "$dir/.vauban-site"; then reload=yes; fi
	if writer test -L "$file" || writer test -d "$file"; then
		echo "VAUBAN_SITE_FOREIGN"
	elif writer test -e "$file"; then
		h=$(writer sha256sum "$file" | cut -d' ' -f1)
		rec=$(as_deploy head -n 1 "$dir/.vauban-site" 2>/dev/null || true)
		pend=$(as_deploy head -n 1 "$dir/.vauban-site.pending" 2>/dev/null || true)
		if [ -n "$h" ] && { [ "$h" = "$rec" ] || [ "$h" = "$pend" ]; }; then
			writer cp -p "$file" "$hold"
			writer rm -f "$file" "$bak"
			if ! out=$(as_deploy docker exec "$container" caddy validate --config /etc/caddy/Caddyfile 2>&1); then
				writer mv -fT "$hold" "$file"
				printf '%s\n' "$out" | tail -n 20 >&2
				exit 6
			fi
			writer rm -f "$hold"
			reload=yes
		else
			echo "VAUBAN_SITE_FOREIGN"
		fi
	fi
	if [ "$reload" = yes ]; then
		if ! as_deploy docker exec "$container" caddy reload --config /etc/caddy/Caddyfile; then
			echo "caddy reload failed" >&2
			exit 7
		fi
	fi
fi

# 3. the project dir: the marker goes last, so a partial removal keeps the
# project provably ours for a rerun.
if as_deploy test -L "$dir"; then
	echo "$dir is a symlink; refusing" >&2
	exit 4
fi
as_deploy find "$dir" -mindepth 1 -maxdepth 1 ! -name .vauban-owner -exec rm -rf -- {} +
as_deploy rm -f -- "$marker"
as_deploy rmdir -- "$dir"

# 4. last: the authorized_keys line of this project (this revokes its key).
remove_key_line
echo "project removed"
