#!/bin/sh
# shellcheck disable=SC1091,SC2012,SC2016
# vauban read-only server audit. Prints key=value lines, never writes, never prompts.
# Usage: sh -s -- PROJECT
set -u
project="${1:-}"

emit() { printf '%s=%s\n' "$1" "$(printf '%s' "$2" | tr -d '\r' | tr '\n' ' ')"; }
have() { command -v "$1" >/dev/null 2>&1; }

# OS
os_id=""; os_like=""; os_pretty=""
if [ -r /etc/os-release ]; then
	os_id=$(. /etc/os-release 2>/dev/null; printf '%s' "${ID:-}")
	os_like=$(. /etc/os-release 2>/dev/null; printf '%s' "${ID_LIKE:-}")
	os_pretty=$(. /etc/os-release 2>/dev/null; printf '%s' "${PRETTY_NAME:-}")
fi
emit os_id "$os_id"
emit os_like "$os_like"
emit os_pretty "$os_pretty"

# sudo
sudo_ok=no
if [ "$(id -u 2>/dev/null)" = 0 ]; then
	sudo_ok=yes
elif have sudo && sudo -n true >/dev/null 2>&1; then
	sudo_ok=yes
fi
emit sudo "$sudo_ok"

# Docker (direct, or via passwordless root when the docker group is missing)
D=""
docker_ok=no; compose_ok=no
if have docker; then
	if docker version >/dev/null 2>&1; then
		D="docker"
	elif [ "$sudo_ok" = yes ] && sudo -n docker version >/dev/null 2>&1; then
		D="sudo -n docker"
	fi
fi
if [ -n "$D" ]; then
	docker_ok=yes
	$D compose version >/dev/null 2>&1 && compose_ok=yes
fi
emit docker "$docker_ok"
emit compose "$compose_ok"
if id -nG 2>/dev/null | tr ' ' '\n' | grep -qx docker; then emit docker_group yes; else emit docker_group no; fi

# deploy user and project
deploy_home=""
if id deploy >/dev/null 2>&1; then
	emit deploy_user yes
	deploy_home=$(getent passwd deploy 2>/dev/null | cut -d: -f6)
else
	emit deploy_user no
fi
emit project "$project"
owner=""
case "$project" in
	"" | */* | .*) ;;
	*)
		if [ -n "$deploy_home" ] && [ -d "$deploy_home/apps/$project" ]; then
			owner="-"
			if [ -r "$deploy_home/apps/$project/.vauban-owner" ]; then
				owner=$(head -n 1 "$deploy_home/apps/$project/.vauban-owner" 2>/dev/null | tr -d ' \r')
				[ -n "$owner" ] || owner="-"
			fi
		fi
		;;
esac
emit project_owner "$owner"
projects=""
if [ -n "$deploy_home" ] && [ -d "$deploy_home/apps" ]; then
	projects=$(ls -1 "$deploy_home/apps" 2>/dev/null | tr '\n' ',' | sed 's/,$//')
fi
emit projects "$projects"

# Docker network
if [ -n "$D" ] && $D network ls --format '{{.Name}}' 2>/dev/null | grep -qx vauban; then
	emit network_vauban yes
else
	emit network_vauban no
fi

# Ports 80/443: container publishing it, else the listening process
ps_out=""
[ -n "$D" ] && ps_out=$($D ps --format '{{.Names}}|{{.Image}}|{{.Ports}}' 2>/dev/null)
for port in 80 443; do
	owner=""
	if [ -n "$ps_out" ]; then
		owner=$(printf '%s\n' "$ps_out" | awk -F'|' -v p=":$port-" 'index($3, p) { print $1; exit }')
	fi
	if [ -z "$owner" ] && have ss; then
		line=$(ss -ltnpH "sport = :$port" 2>/dev/null | head -n 1)
		if [ -n "$line" ]; then
			owner=$(printf '%s' "$line" | sed -n 's/.*users:(("\([^"]*\)".*/\1/p')
			[ -n "$owner" ] || owner="unknown"
		fi
	fi
	emit "port_$port" "$owner"
done

# Caddy container: image "caddy" (any tag/registry) publishing 80 or 443
caddy=""
if [ -n "$ps_out" ]; then
	caddy=$(printf '%s\n' "$ps_out" | awk -F'|' '
		{
			img = $2; sub(/:[^\/]*$/, "", img); sub(/@.*/, "", img); n = split(img, seg, "/")
			if (seg[n] == "caddy" && ($3 ~ /:80-/ || $3 ~ /:443-/)) {
				if ($1 == "vauban-caddy") { best = $1; exit }
				if (best == "") best = $1
			}
		}
		END { print best }')
fi
emit caddy_container "$caddy"
if [ -n "$caddy" ]; then
	emit caddy_mounts "$($D inspect -f '{{json .Mounts}}' "$caddy" 2>/dev/null)"
	emit caddy_networks "$($D inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$caddy" 2>/dev/null)"
	if $D exec "$caddy" cat /etc/caddy/Caddyfile 2>/dev/null | grep -qE '^[[:space:]]*import[[:space:]]+/etc/caddy/sites/\*\.caddy[[:space:]]*$'; then
		emit caddy_import yes
	else
		emit caddy_import no
	fi
fi

# Firewall (ufw only, readable status needs root; otherwise unknown)
fw=none
if have ufw; then
	st=$(ufw status 2>/dev/null)
	case "$st" in
		*"Status: inactive"*) fw=inactive ;;
		*"Status: active"*)
			if printf '%s\n' "$st" | grep -E '^(80|443)([/ ,]|$)' | grep -q ALLOW; then fw=active-open; else fw=active-closed; fi
			;;
		*) fw=unknown ;;
	esac
fi
emit firewall "$fw"

# sshd password auth (needs sudo)
pw=unknown
if [ "$sudo_ok" = yes ] && [ "$(id -u 2>/dev/null)" != 0 ] && have sudo; then
	v=$(sudo -n sshd -T 2>/dev/null | awk '$1 == "passwordauthentication" { print $2 }')
	[ -n "$v" ] && pw="$v"
fi
emit ssh_pwauth "$pw"

# Disk and updates
emit disk_free_gb "$(df -P -k / 2>/dev/null | awk 'NR == 2 { print int($4 / 1048576) }')"
upd=0
if have apt; then
	upd=$(apt list --upgradable 2>/dev/null | grep -c '/')
fi
emit pending_updates "$upd"
exit 0
