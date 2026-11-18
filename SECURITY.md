# Security policy

vauban creates servers, firewalls, SSH keys, CI/CD workflows and GitHub secrets, so security reports are taken seriously.

## Reporting a vulnerability

**Please do not open a public issue.** Report it privately through GitHub Security Advisories:

👉 [Report a vulnerability](https://github.com/jean-baptisteAZAN/vauban/security/advisories/new)

Include what you found, how to reproduce it (a minimal repo and the commands you ran help a lot), the impact you see, and the `vauban version` you used. You should get a first answer within a week. Once a fix is released, the advisory is published and you are credited unless you prefer otherwise.

## Supported versions

vauban is young: only the latest release gets security fixes.

## Scope

In scope, everything vauban ships or generates:

- the `vauban` binary itself (credential handling, the commands it runs, its state file `.vauban/state.json`);
- generated **GitHub Actions workflows** (`ci.yml`, `deploy.yml`): for example a way for a pull request or a fork to trigger a deploy or read the `DEPLOY_*` secrets;
- generated **cloud-init** and **OpenTofu** code: SSH hardening, firewall rules, the `deploy` user, unattended upgrades;
- generated `Dockerfile`, `compose.yaml` and `Caddyfile`;
- any secret value (token, private key) printed, logged, or written to the repository or the state file.

Out of scope: vulnerabilities in the tools and services vauban delegates to (OpenTofu and its providers, `gh`, Docker, Caddy, Debian, the cloud providers, GitHub) — please report those upstream — and issues that need an attacker who already controls your machine or your GitHub account.
