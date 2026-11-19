# Contributing to vauban

Thanks for taking the time! Bug reports, ideas and pull requests are all welcome.

## Before you start

- For a bug, [open an issue](https://github.com/jean-baptisteAZAN/vauban/issues/new?template=bug_report.yml) with `vauban version`, your OS and what you ran.
- For a new feature, a new cloud or a new stack, open a [feature request](https://github.com/jean-baptisteAZAN/vauban/issues/new?template=feature_request.yml) first so we can agree on the shape before you write code.
- Security issues go through [SECURITY.md](SECURITY.md), never a public issue.

## Dev setup

You need **Go 1.27** and [golangci-lint](https://golangci-lint.run/welcome/install/) v2.

```sh
git clone https://github.com/jean-baptisteAZAN/vauban
cd vauban
go build -o vauban .        # or: make build
./vauban doctor --dir testdata/go-app
```

`tofu`, `gh`, `docker` and `ssh-keygen` are only needed to run `apply` for real. The test suite never calls them, nor any cloud or GitHub API: commands go through a fake runner and cloud APIs through local test servers.

## Checks

Run both before opening a pull request; CI runs the same:

```sh
go test -race ./...
golangci-lint run
```

CI does not run on draft pull requests. Open your PR as a draft while you
iterate, then mark it **Ready for review**: CI runs then, and on every later
push (a newer push cancels the older run). `main` only accepts PRs whose
checks are green.

### End-to-end test (bring your own server)

`internal/e2e` drives the real `audit`, server steps and `destroy` script over real ssh against a throwaway Debian container (sshd, sudo, Docker). It is skipped unless `VAUBAN_E2E=1` and needs Docker and `ssh-keygen`:

```sh
VAUBAN_E2E=1 go test -tags e2e ./internal/e2e/ -v -timeout 15m
```

The container runs `--privileged` (it runs its own dockerd), is published only on `127.0.0.1` on a random port, never gets the host's Docker socket, and is labelled `vauban-e2e` and removed at the end (`docker rm -f`). If the run is killed (a Go timeout panic skips cleanup), remove strays with `docker rm -f $(docker ps -aq --filter label=vauban-e2e)`. Keys and `HOME` are temporary. The first run builds the image and pulls `caddy:2` inside the container, so it needs network access.

### Golden files

Generated files (Dockerfiles, workflows, Caddyfile, compose, OpenTofu, cloud-init) are compared against golden files under `testdata/golden/`. When you change a template on purpose, regenerate them and review the diff:

```sh
go test ./internal/module/... ./internal/cloud/... -update
git diff testdata/golden
```

### The demo GIF

`docs/demo.gif` is recorded with [vhs](https://github.com/charmbracelet/vhs) from `docs/demo.tape`, against fake `tofu`/`gh`/`docker`/`ssh-keygen` and stubbed cloud APIs (see `docs/demo/setup.sh`): it never touches a real cloud, GitHub or your `$HOME`.

```sh
vhs docs/demo.tape
```

## Guidelines

- Keep vauban fast and small: `doctor` must stay well under 50 ms and the binary under 20 MB.
- Delegate to official tools and providers rather than adding hand-written templates or API clients.
- Anything vauban writes or runs must show up in `vauban plan` first, and must never print or store secret values.
- Add or update tests with every change; goldens for generated output.
- Update `README.md` when behaviour, flags or defaults change. Every claim in it should stay true.

## Commit style

[Conventional Commits](https://www.conventionalcommits.org/), with an optional scope:

```
feat(cloud): add region listing for scaleway
fix(ci): poetry CI installs dependencies with --no-root
docs: explain the demo setup
```

Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`. Keep the subject short and imperative; explain the *why* in the body when it is not obvious.
