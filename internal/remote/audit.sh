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

# Docker: direct, or read-only probes through passwordless root when the docker group is missing
D=""
docker_ok=no; compose_ok=no; access=absent
emit ssh_user "$(id -un 2>/dev/null)"
if have docker; then
	derr=$(docker version 2>&1 >/dev/null)
	if docker version >/dev/null 2>&1; then
		D="docker"; access=direct
	elif [ "$sudo_ok" = yes ] && sudo -n docker version >/dev/null 2>&1; then
		D="sudo -n docker"; access=sudo
	else
		case "$derr" in
			*ermission*) access=denied ;;
			*) access=unavailable ;;
		esac
	fi
fi
if [ -n "$D" ]; then
	docker_ok=yes
	$D compose version >/dev/null 2>&1 && compose_ok=yes
fi
emit docker "$docker_ok"
emit docker_access "$access"
emit compose "$compose_ok"
if id -nG 2>/dev/null | tr ' ' '\n' | grep -qx docker; then emit docker_group yes; else emit docker_group no; fi

# deploy user and project
deploy_home=""
if id deploy >/dev/null 2>&1; then
	emit deploy_user yes
	deploy_home=$(getent passwd deploy 2>/dev/null | cut -d: -f6)
	if id -nG deploy 2>/dev/null | tr ' ' '\n' | grep -qx docker; then emit deploy_docker_group yes; else emit deploy_docker_group no; fi
else
	emit deploy_user no
	emit deploy_docker_group no
fi
emit project "$project"
owner=""
case "$project" in
	"" | */* | .*) ;;
	*)
		# "?" means unknown: ~deploy is often not traversable by a non-root ssh user
		if [ -n "$deploy_home" ]; then
			pd="$deploy_home/apps/$project"
			if [ ! -x "$deploy_home" ]; then
				owner="?"
			elif [ -d "$deploy_home/apps" ]; then
				if [ ! -x "$deploy_home/apps" ]; then
					owner="?"
				elif [ -d "$pd" ]; then
					if [ ! -x "$pd" ]; then
						owner="?"
					elif [ -e "$pd/.vauban-owner" ]; then
						if [ -r "$pd/.vauban-owner" ]; then
							owner=$(head -n 1 "$pd/.vauban-owner" 2>/dev/null | tr -d ' \r')
							[ -n "$owner" ] || owner="-"
						else
							owner="?"
						fi
					else
						owner="-"
					fi
				fi
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
	if $D exec "$caddy" test -d /etc/caddy/sites >/dev/null 2>&1; then
		emit caddy_sites_dir yes
	else
		emit caddy_sites_dir no
	fi
	if $D exec "$caddy" cat /etc/caddy/Caddyfile 2>/dev/null | grep -qE '^[[:space:]]*import[[:space:]]+/etc/caddy/sites/\*\.caddy[[:space:]]*$'; then
		emit caddy_import yes
	else
		emit caddy_import no
	fi
fi

# Firewall (ufw only, readable status needs root; otherwise unknown)
fw=none
ufw_bin=""
if have ufw; then ufw_bin=ufw; elif [ -x /usr/sbin/ufw ]; then ufw_bin=/usr/sbin/ufw; fi
if [ -n "$ufw_bin" ]; then
	st=$("$ufw_bin" status 2>/dev/null)
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
emit audit_done 1
exit 0
