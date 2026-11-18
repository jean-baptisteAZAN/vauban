package detect

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var githubRemote = regexp.MustCompile(`^(?:git@github\.com:|(?:https?|ssh|git)://(?:[^@/]+@)?github\.com[/:])([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)

// parseGitHubRemote returns "owner/repo" for GitHub URLs (https or ssh), else "".
func parseGitHubRemote(url string) string {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

func detectGit(p *Project) {
	gitPath := filepath.Join(p.Root, ".git")
	st, err := os.Stat(gitPath)
	if err != nil {
		return
	}
	p.IsGitRepo = true
	if !st.IsDir() { // worktree/submodule: .git is a file
		return
	}
	f, err := os.Open(filepath.Join(gitPath, "config"))
	if err != nil {
		return
	}
	defer f.Close()
	inOrigin := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "url" {
			p.GitHubRepo = parseGitHubRemote(v)
			return
		}
	}
}
