# shellcheck shell=sh
# shellcheck disable=SC2086
# Manages <sites dir>/<project>.caddy and reloads the shared Caddy.
# $1 = action (write|commit|rollback|validate|reload), $2 = mode ("sudo" or
# "direct"), $3 = sites dir (absolute, one line), $4 = project, $5 = repo
# slug, $6 = caddy container, $7 = base64 site file (write, commit, rollback).
# Ownership of the site file is proven by its sha256 recorded in
# ~deploy/apps/<project>/.vauban-site after a successful validate: a file
# whose hash differs is never overwritten or removed (exit 8,
# "VAUBAN_SITE_FOREIGN" on stdout). write records the new hash in
# .vauban-site.pending before it renames the file into place, so a crash
# before commit leaves a file that still counts as ours. write also needs the project owner marker
# to equal the slug, ignoring case (exit 3) and refuses a symlink or directory (exit 4).
# write backs the previous file up to .vauban-<project>.bak, rollback restores
# it (or removes the new file). validate/reload run caddy inside its
# container and never restart or recreate it.
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
hash_of() { writer sha256sum "$1" | cut -d' ' -f1; }

file="$sites/$project.caddy"
bak="$sites/.vauban-$project.bak"
tmp=""
dec=""
hf=""
cleanup() {
	[ -z "$dec" ] || rm -f "$dec"
	[ -z "$hf" ] || rm -f "$hf"
	[ -z "$tmp" ] || writer rm -f "$tmp" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

need_home() {
	home=$(getent passwd deploy | cut -d: -f6)
	if [ -z "$home" ]; then
		echo "deploy user does not exist" >&2
		exit 1
	fi
	marker="$home/apps/$project/.vauban-owner"
	site_hash="$home/apps/$project/.vauban-site"
	pending="$home/apps/$project/.vauban-site.pending"
}
lc() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
# need_ours: the project's owner marker is this repo (case-insensitive).
need_ours() {
	cur=$(as_deploy head -n 1 "$marker" 2>/dev/null || true)
	if [ -z "$cur" ] || [ "$(lc "$cur")" != "$(lc "$slug")" ]; then
		echo "VAUBAN_OWNER_CONFLICT ${cur:--}"
		exit 3
	fi
}
# decode: the base64 arg into $dec, and its sha256 into $want.
decode() {
	dec=$(mktemp)
	printf '%s' "$b64" | base64 -d >"$dec"
	if [ ! -s "$dec" ]; then
		echo "empty site content" >&2
		exit 2
	fi
	want=$(sha256sum <"$dec" | cut -d' ' -f1)
	if [ -z "$want" ]; then
		echo "cannot hash site content" >&2
		exit 2
	fi
}
# record <hash> <path>: write one line as deploy, failing loudly.
record() {
	if [ -z "$1" ]; then
		echo "refusing to record an empty hash" >&2
		exit 9
	fi
	hf=$(mktemp)
	printf '%s\n' "$1" >"$hf"
	as_deploy tee "$2" <"$hf" >/dev/null
}
# is_ours_file: the site file on disk is one vauban recorded (committed or
# pending) or exactly the content about to be written.
is_ours_file() {
	h=$(hash_of "$file")
	[ -n "$h" ] || return 1
	rec=$(as_deploy head -n 1 "$site_hash" 2>/dev/null || true)
	pend=$(as_deploy head -n 1 "$pending" 2>/dev/null || true)
	[ "$h" = "$rec" ] || [ "$h" = "$pend" ] || [ "$h" = "$want" ]
}

case "$action" in
write)
	pick_writer
	need_home
	need_ours
	if writer test -L "$file" || writer test -d "$file"; then
		echo "$file is a symlink or directory; refusing" >&2
		exit 4
	fi
	decode
	if writer test -e "$file"; then
		if ! is_ours_file; then
			echo "VAUBAN_SITE_FOREIGN"
			exit 8
		fi
		# a pending record means the file on disk was never validated: keep
		# the backup of the last good one.
		if ! as_deploy test -e "$pending"; then
			writer cp -p "$file" "$bak"
		fi
	elif ! as_deploy test -e "$pending"; then
		writer rm -f "$bak"
	fi
	tmp=$(writer mktemp "$sites/.vauban-$project.XXXXXX")
	writer tee "$tmp" <"$dec" >/dev/null
	writer chmod 644 "$tmp"
	record "$want" "$pending"
	writer mv -fT "$tmp" "$file"
	tmp=""
	echo "site written"
	;;
commit)
	pick_writer
	need_home
	need_ours
	decode
	if [ "$(hash_of "$file")" != "$want" ]; then
		echo "site file on disk is not the content vauban wrote; not recording it" >&2
		exit 9
	fi
	record "$want" "$site_hash"
	as_deploy rm -f "$pending"
	writer rm -f "$bak"
	echo "site recorded"
	;;
rollback)
	pick_writer
	need_home
	need_ours
	decode
	# only undo our own write: the file must still hold the content we wrote.
	if writer test -f "$file" && ! writer test -L "$file"; then
		if [ "$(hash_of "$file")" = "$want" ]; then
			if writer test -f "$bak"; then
				writer mv -fT "$bak" "$file"
			else
				writer rm -f "$file"
			fi
		fi
	fi
	as_deploy rm -f "$pending"
	echo "site rolled back"
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
