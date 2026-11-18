package cli

import (
	"errors"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/detect"
)

// createRepoCmd is the gh command that creates a private GitHub repository
// for p from the current directory and pushes it.
func createRepoCmd(p *detect.Project) string {
	return "gh repo create " + p.Name + " --private --source . --push"
}

// requireRemote fails (exit 1) when p has no GitHub remote: deploys run from
// GitHub Actions, so plan and apply check it before asking anything.
func requireRemote(p *detect.Project) error {
	if p.GitHubRepo != "" {
		return nil
	}
	create := `git add -A && git commit -m "init" && ` + createRepoCmd(p)
	if !p.IsGitRepo {
		create = "git init && " + create
	}
	lines := []string{
		"no GitHub remote: vauban deploys from GitHub Actions.",
		"  create one:  " + create,
		"  or link one: git remote add origin git@github.com:<owner>/<repo>.git",
	}
	if !p.HasCommits {
		lines = append(lines, "  this repo has no commits yet: gh repo create --push needs at least one commit")
	}
	return &ExitError{Code: 1, Err: errors.New(strings.Join(lines, "\n"))}
}
