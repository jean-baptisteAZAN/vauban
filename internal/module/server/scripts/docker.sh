# shellcheck shell=sh
# Installs Docker Engine and the Compose plugin from Docker's official apt
# repository. Needs passwordless sudo. Idempotent: does nothing when both exist.
set -eu

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
	echo "docker already installed"
	exit 0
fi

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
export DEBIAN_FRONTEND=noninteractive

sudo -n apt-get update
sudo -n apt-get install -y ca-certificates curl
sudo -n install -m 0755 -d /etc/apt/keyrings
sudo -n curl -fsSL "https://download.docker.com/linux/$repo_os/gpg" -o /etc/apt/keyrings/docker.asc
sudo -n chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$arch signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$repo_os $codename stable" |
	sudo -n tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo -n apt-get update
sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo -n systemctl enable --now docker >/dev/null 2>&1 || true
