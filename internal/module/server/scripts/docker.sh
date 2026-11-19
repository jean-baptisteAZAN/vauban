# shellcheck shell=sh
# shellcheck disable=SC2086
# Installs Docker Engine and the Compose plugin from Docker's official apt
# repository. $1 = mode: "full" (Docker absent) or "compose" (Docker present,
# Compose plugin missing). Needs root or passwordless sudo.
#
# An existing Docker is never disturbed: with docker already on the machine
# only docker-compose-plugin is installed (never upgraded engine packages), and
# only when a download.docker.com apt source is already configured. An existing
# Docker apt source or key is reused, never overwritten. Idempotent.
set -eu

mode=${1:-full}
if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi

have_docker=no
command -v docker >/dev/null 2>&1 && have_docker=yes
if [ "$have_docker" = yes ] && docker compose version >/dev/null 2>&1; then
	echo "docker already installed"
	exit 0
fi
# Docker already there (whatever the caller believed): compose only.
[ "$have_docker" = yes ] && mode=compose

apt() {
	$SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get "$@" </dev/null
}

has_docker_source() {
	grep -rqs download.docker.com /etc/apt/sources.list /etc/apt/sources.list.d/
}

if [ "$mode" = compose ]; then
	if ! has_docker_source; then
		echo "Docker is installed but the Compose plugin is missing: install docker-compose-plugin (Docker repo) or docker-compose-v2 (distro) by hand" >&2
		exit 1
	fi
	apt update
	apt install -y --no-upgrade docker-compose-plugin
	exit 0
fi

if ! has_docker_source; then
	# shellcheck disable=SC1091
	. /etc/os-release
	case "${ID:-}" in
	debian | ubuntu) repo_os=$ID codename=${VERSION_CODENAME:-} ;;
	*)
		case " ${ID_LIKE:-} " in
		*" ubuntu "*) repo_os=ubuntu codename=${UBUNTU_CODENAME:-${VERSION_CODENAME:-}} ;;
		*" debian "*) repo_os=debian codename=${VERSION_CODENAME:-} ;;
		*)
			echo "unsupported OS ${ID:-unknown}: install Docker by hand" >&2
			exit 1
			;;
		esac
		;;
	esac
	if [ -z "$codename" ]; then
		echo "cannot determine the OS codename: install Docker by hand" >&2
		exit 1
	fi
	arch=$(dpkg --print-architecture)

	apt update
	apt install -y --no-upgrade ca-certificates curl
	$SUDO install -m 0755 -d /etc/apt/keyrings
	$SUDO curl -fsSL "https://download.docker.com/linux/$repo_os/gpg" -o /etc/apt/keyrings/docker.asc
	$SUDO chmod a+r /etc/apt/keyrings/docker.asc
	echo "deb [arch=$arch signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$repo_os $codename stable" |
		$SUDO tee /etc/apt/sources.list.d/docker.list >/dev/null
fi

apt update
apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
$SUDO systemctl enable --now docker >/dev/null 2>&1 || true
