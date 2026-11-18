package deploy

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stepRun returns the run script of the first deploy-job step matching keep.
func stepRun(t *testing.T, keep func(run string) bool) string {
	t.Helper()
	w := parseWorkflow(t, file(t, plan(t, goApp(t), cfg()), ".github/workflows/deploy.yml"))
	for _, s := range w.Jobs["deploy"].Steps {
		if s.Run != "" && keep(s.Run) {
			return s.Run
		}
	}
	t.Fatal("no matching step")
	return ""
}

// fakeBins writes executable shell scripts into a new temp dir.
func fakeBins(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runScript(t *testing.T, dir, script string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTipCheckScript(t *testing.T) {
	script := stepRun(t, func(r string) bool { return strings.Contains(r, "gh api") })
	bin := fakeBins(t, map[string]string{
		"gh": `[ "$*" = "api repos/acme/widget/commits/main --jq .sha" ] || exit 9; printf '%s\n' "$FAKE_TIP"` + "\n",
	})
	for _, tc := range []struct {
		tip, want, log string
	}{
		{"abc123", "current=true\n", ""},
		{"def456", "current=false\n", "skipping: abc123 is no longer the tip of main"},
	} {
		dir := t.TempDir()
		output := filepath.Join(dir, "out")
		got, err := runScript(t, dir, script,
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GITHUB_OUTPUT="+output, "IMAGE_TAG=abc123", "REPO=acme/widget", "GH_TOKEN=t", "FAKE_TIP="+tc.tip)
		if err != nil {
			t.Fatalf("%v: %s", err, got)
		}
		b, _ := os.ReadFile(output)
		if string(b) != tc.want || !strings.Contains(got, tc.log) {
			t.Errorf("tip %s: output %q, log %q", tc.tip, b, got)
		}
	}
}

// TestDeployScriptHostileValues runs the SSH deploy step against a fake
// remote (ssh runs the command locally with bash) and feeds shell
// metacharacters through DEPLOY_DOMAIN, IMAGE_TAG and GITHUB_ACTOR.
func TestDeployScriptHostileValues(t *testing.T) {
	script := stepRun(t, func(r string) bool { return strings.Contains(r, "scp ") })
	const token = "ghs_registry_token_value"
	hostile := "x; touch PWNED1 $(touch PWNED2) `touch PWNED3` 'q\" \\ && touch PWNED4"
	root := t.TempDir()
	work, home, remote, tmp := filepath.Join(root, "work"), filepath.Join(root, "home"), filepath.Join(root, "remote"), filepath.Join(root, "tmp")
	for _, d := range []string{filepath.Join(work, "deploy"), home, remote, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"deploy/compose.yaml": "c", "deploy/Caddyfile": "k", ".env": "A='b'\n"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dockerLog := filepath.Join(root, "docker.log")
	bin := fakeBins(t, map[string]string{
		"ssh-keyscan": "printf '|1|fake ssh-ed25519 AAAA\\n'\n",
		"cloud-init":  "exit 0\n",
		// macOS mktemp ignores TMPDIR; pin it so cleanup can be checked.
		"mktemp": "[ \"$*\" = -d ] || exit 9\nexec /usr/bin/mktemp -d \"$TMPDIR/docker.XXXXXX\"\n",
		"ssh":    "for last; do :; done\ncd \"$REMOTE_HOME\" && HOME=\"$REMOTE_HOME\" exec bash -c \"$last\"\n",
		"scp": `args=("$@"); n=${#args[@]}
mkdir -p "$REMOTE_HOME/app"
for ((i=0; i<n-1; i++)); do
  case ${args[i]} in -i|-o) i=$((i+1)) ;; *) cp "${args[i]}" "$REMOTE_HOME/app/" ;; esac
done
`,
		"docker": `{ printf 'ARGS=%s\n' "$*"; printf 'DOMAIN=%s\n' "$DOMAIN"; printf 'IMAGE_TAG=%s\n' "$IMAGE_TAG"
  printf 'APP_PORT=%s\n' "$APP_PORT"; printf 'DOCKER_CONFIG=%s\n' "$DOCKER_CONFIG"; } >> "$DOCKER_LOG"
if [ "$1" = login ]; then cat > "$DOCKER_CONFIG/config.json"; fi
`,
	})
	out, err := runScript(t, work, script,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home, "TMPDIR="+tmp, "REMOTE_HOME="+remote, "DOCKER_LOG="+dockerLog,
		"DEPLOY_HOST=203.0.113.9", "DEPLOY_SSH_KEY=PRIVATE-KEY", "GHCR_TOKEN="+token,
		"DEPLOY_DOMAIN="+hostile+"d", "IMAGE_TAG="+hostile+"i", "GITHUB_ACTOR="+hostile+"a", "APP_PORT=8080")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), "PWNED") {
			t.Errorf("hostile value executed: %s", path)
		}
		if err == nil && !d.IsDir() && path != dockerLog {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), token) {
				t.Errorf("registry token persisted in %s", path)
			}
		}
		return nil
	})
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("throwaway docker config not removed: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(remote, ".docker")); err == nil {
		t.Error("~/.docker created on the server")
	}

	b, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, want := range []string{
		"ARGS=login ghcr.io -u " + hostile + "a --password-stdin\n",
		"ARGS=compose pull\nDOMAIN=" + hostile + "d\nIMAGE_TAG=" + hostile + "i\nAPP_PORT=8080\nDOCKER_CONFIG=" + tmp,
		"ARGS=compose up -d --remove-orphans\nDOMAIN=" + hostile + "d\nIMAGE_TAG=" + hostile + "i\n",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("docker log lacks %q:\n%s", want, log)
		}
	}
	if st, err := os.Stat(filepath.Join(remote, "app", ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("remote .env: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Join(home, ".ssh", "deploy_key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("local deploy key: %v %v", st, err)
	}
}
