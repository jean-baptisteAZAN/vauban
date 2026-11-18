# Sets up a sandbox for recording docs/demo.gif (see docs/demo.tape).
#
# Source it from the repository root:   source docs/demo/setup.sh
#
# Nothing here touches a real cloud, GitHub or your real $HOME:
#   - vauban is built with -tags demo (docs/demo/vauban-demo): the real CLI,
#     with the cloud API calls (auth check, regions, sizes) stubbed;
#   - tofu, gh, docker and ssh-keygen on PATH are fake scripts that print
#     plausible output and write the files vauban reads back;
#   - HOME is a fresh temporary directory (the deploy key lands there);
#   - the project is a copy of testdata/go-app with a fake GitHub origin.

set -e
DEMO_REPO=$(pwd)
DEMO=$(mktemp -d /tmp/vauban-demo.XXXX) # short path: it shows in the demo
mkdir -p "$DEMO/bin" "$DEMO/home"

# Build before HOME changes, so the Go build cache is reused.
go build -tags demo -o "$DEMO/bin/vauban" "$DEMO_REPO/docs/demo/vauban-demo"

cat >"$DEMO/bin/tofu" <<'EOF'
#!/bin/sh
# fake OpenTofu: answers what vauban runs, never calls a provider
case "$1" in -chdir=*) cd "${1#-chdir=}" && shift ;; esac
case "$1" in
version) echo "OpenTofu v1.10.7 (demo fake)" ;;
init)
  sleep 1
  echo "OpenTofu has been successfully initialized!" ;;
plan)
  sleep 1.2
  : >tfplan
  echo "OpenTofu will perform the following actions:"
  echo "  + hcloud_firewall.vauban"
  echo "  + hcloud_server.vauban"
  echo "  + hcloud_ssh_key.vauban[0]"
  echo "Plan: 3 to add, 0 to change, 0 to destroy." ;;
show)
  printf '%s\n' '{"format_version":"1.2","resource_changes":[{"address":"hcloud_firewall.vauban","change":{"actions":["create"]}},{"address":"hcloud_server.vauban","change":{"actions":["create"]}},{"address":"hcloud_ssh_key.vauban[0]","change":{"actions":["create"]}}]}' ;;
apply)
  sleep 2.5
  echo "Apply complete! Resources: 3 added, 0 changed, 0 destroyed." ;;
output)
  printf '%s\n' '{"server_ip":{"sensitive":false,"type":"string","value":"203.0.113.42"},"server_ipv6":{"sensitive":false,"type":"string","value":"2001:db8::1"}}' ;;
*) echo "fake tofu: unsupported: $*" >&2; exit 1 ;;
esac
EOF

cat >"$DEMO/bin/gh" <<'EOF'
#!/bin/sh
# fake GitHub CLI: pretends to be logged in and to store secrets
case "$1 $2" in
"auth status") echo "github.com: logged in as demo (fake)" ;;
"secret set")
  cat >/dev/null
  sleep 0.4
  echo "✓ Set Actions secret $3 for $5" ;;
"secret delete") echo "✓ Deleted Actions secret $3 from $5" ;;
*) echo "fake gh: unsupported: $*" >&2; exit 1 ;;
esac
EOF

cat >"$DEMO/bin/docker" <<'EOF'
#!/bin/sh
echo "Docker version 28.0.0 (demo fake)"
EOF

cat >"$DEMO/bin/ssh-keygen" <<'EOF'
#!/bin/sh
# fake ssh-keygen: writes a dummy (unusable) key pair at -f <path>
while [ $# -gt 0 ]; do
  case "$1" in -f) path=$2; shift ;; -C) comment=$2; shift ;; esac
  shift
done
[ -n "$path" ] || { echo "fake ssh-keygen: -f required" >&2; exit 1; }
sleep 0.3
printf '%s\n' "-----BEGIN OPENSSH PRIVATE KEY-----" "demo key, not a real key" "-----END OPENSSH PRIVATE KEY-----" >"$path"
chmod 600 "$path"
echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoDemoDemoDemoDemoDemoDemoDemoDemoDemo ${comment:-demo}" >"$path.pub"
EOF
chmod +x "$DEMO/bin/"*

cp -R "$DEMO_REPO/testdata/go-app" "$DEMO/hello-api"
mkdir -p "$DEMO/hello-api/.git"
printf '%s\n' "ref: refs/heads/main" >"$DEMO/hello-api/.git/HEAD"
cat >"$DEMO/hello-api/.git/config" <<'EOF'
[core]
	repositoryformatversion = 0
[remote "origin"]
	url = git@github.com:acme/hello-api.git
	fetch = +refs/heads/*:refs/remotes/origin/*
EOF

unset GH_TOKEN GITHUB_TOKEN DIGITALOCEAN_TOKEN SCW_ACCESS_KEY SCW_SECRET_KEY \
  SCW_DEFAULT_PROJECT_ID SCW_CONFIG_PATH SCW_PROFILE XDG_CONFIG_HOME NO_COLOR
export HOME="$DEMO/home"
export PATH="$DEMO/bin:/usr/bin:/bin"
export HCLOUD_TOKEN=demo-token
export PS1='\[\e[38;5;141m\]❯\[\e[0m\] '
cd "$DEMO/hello-api"
set +e
