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

// commitCmd is the first commit gh repo create --push needs, with git init
// first when p is not a git repository yet.
func commitCmd(p *detect.Project) string {
	cmd := `git add -A && git commit -m "init"`
	if !p.IsGitRepo {
		cmd = "git init && " + cmd
	}
	return cmd
}

// remoteSteps are the commands that give p a GitHub remote: a first commit
// when there is none (gh repo create --push needs one), then gh repo create.
func remoteSteps(p *detect.Project) (commit, create string) {
	if !p.HasCommits {
		commit = commitCmd(p)
	}
	return commit, createRepoCmd(p)
}

// requireRemote fails (exit 1) when p has no GitHub remote: deploys run from
// GitHub Actions, so plan and apply check it before asking anything.
func requireRemote(p *detect.Project) error {
	if p.GitHubRepo != "" {
		return nil
	}
	commit, create := remoteSteps(p)
	lines := []string{"no GitHub remote: vauban deploys from GitHub Actions."}
	if commit != "" {
		lines = append(lines, "  commit your code first (check .gitignore covers .env and secrets): "+commit)
	}
	lines = append(lines,
		"  create one:  "+create,
		"  or link one: git remote add origin git@github.com:<owner>/<repo>.git",
	)
	return &ExitError{Code: 1, Err: errors.New(strings.Join(lines, "\n"))}
}
