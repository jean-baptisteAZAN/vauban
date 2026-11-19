# shellcheck shell=sh
# shellcheck disable=SC2086
# Creates ~deploy/apps/<project> (0750, owned by deploy) with a .vauban-owner
# marker holding the repository slug. $1 = mode ("sudo" or "direct"),
# $2 = project, $3 = repo slug. A directory owned by another slug, or without
# a marker, or a symlink, is never touched: exit 3 with
# "VAUBAN_OWNER_CONFLICT <other>" on stdout, exit 4 for a symlink.
set -eu

mode=$1
project=$2
slug=$3

case "$project" in
'' | */* | .*)
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

if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi
if [ "$mode" = sudo ] && [ "$(id -un)" != deploy ]; then
	as_deploy() { $SUDO runuser -u deploy -- "$@"; }
else
	as_deploy() { "$@"; }
fi

home=$(getent passwd deploy | cut -d: -f6)
if [ -z "$home" ]; then
	echo "deploy user does not exist" >&2
	exit 1
fi

as_deploy sh -s -- "$home" "$project" "$slug" <<'INNER'
set -eu
umask 022
home=$1
project=$2
slug=$3
apps="$home/apps"
dir="$apps/$project"
marker="$dir/.vauban-owner"

if [ -L "$apps" ] || [ -L "$dir" ] || [ -L "$marker" ]; then
	echo "$dir or its marker is a symlink; refusing" >&2
	exit 4
fi
if [ -d "$dir" ]; then
	if [ -f "$marker" ]; then
		cur=$(head -n 1 "$marker")
		if [ "$cur" = "$slug" ]; then
			chmod 750 "$dir"
			echo "project dir ready"
			exit 0
		fi
		echo "VAUBAN_OWNER_CONFLICT $cur"
		exit 3
	fi
	echo "VAUBAN_OWNER_CONFLICT -"
	exit 3
fi
if [ -e "$dir" ]; then
	echo "$dir exists and is not a directory; refusing" >&2
	exit 4
fi
mkdir -p "$apps"
# build the dir aside, then move it into place: a crash never leaves a
# marker-less project dir.
new=$(mktemp -d "$apps/.vauban-new.XXXXXX")
trap 'rm -rf "$new"' EXIT INT TERM
printf '%s\n' "$slug" >"$new/.vauban-owner"
chmod 644 "$new/.vauban-owner"
chmod 750 "$new"
if [ -e "$dir" ] || [ -L "$dir" ]; then
	echo "VAUBAN_OWNER_CONFLICT -"
	exit 3
fi
mv -T "$new" "$dir"
echo "project dir created"
INNER
