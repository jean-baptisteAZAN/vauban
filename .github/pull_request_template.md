## What and why

<!-- What does this change, and why? Link the issue: "Fixes #123". -->

## How it was tested

<!-- Commands run, repos tried, clouds touched (if any). -->

## Checklist

- [ ] `go test -race ./...` passes
- [ ] `golangci-lint run` passes
- [ ] Golden files regenerated (`-update`) and the diff reviewed, if templates changed
- [ ] Anything new that vauban writes or runs appears in `vauban plan`
- [ ] No secret value is printed, logged or written to the repo
- [ ] README updated if behaviour, flags or defaults changed
