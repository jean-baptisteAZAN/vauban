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
		p.HasCommits = true // a linked worktree always has a checked-out commit
		return
	}
	p.HasCommits = headHasCommit(gitPath)
	f, err := os.Open(filepath.Join(gitPath, "config"))
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck // read-only file, nothing to flush
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

// headHasCommit reports whether .git/HEAD resolves to a commit: a detached
// HEAD, or a branch ref present as a loose file or in packed-refs.
func headHasCommit(gitDir string) bool {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return false
	}
	head := strings.TrimSpace(string(b))
	ref, ok := strings.CutPrefix(head, "ref: ")
	if !ok {
		return head != "" // detached HEAD: a commit id
	}
	ref = strings.TrimSpace(ref)
	if _, err := os.Stat(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		return true
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(packed)) {
		if _, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref {
			return true
		}
	}
	return false
}
