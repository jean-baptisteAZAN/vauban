package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteFileSkipsExisting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Dockerfile")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	env := &Env{Root: root, Log: &log}
	step := WriteFile{Path: "Dockerfile", Content: []byte("new"), Mode: 0o644}
	if step.ID() != "file:Dockerfile" {
		t.Fatalf("ID = %q", step.ID())
	}
	if _, err := step.Apply(context.Background(), env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("existing file changed without Force: %q", b)
	}
	if !strings.Contains(log.String(), "skip (exists) Dockerfile") {
		t.Fatalf("log = %q, want skip (exists) Dockerfile", log.String())
	}

	env.Force = true
	if _, err := step.Apply(context.Background(), env); err != nil {
		t.Fatalf("Apply force: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("file not overwritten with Force: %q", b)
	}

	exec := WriteFile{Path: "scripts/deploy.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755}
	if _, err := exec.Apply(context.Background(), &Env{Root: root}); err != nil {
		t.Fatalf("Apply new: %v", err)
	}
	fi, err := os.Stat(filepath.Join(root, "scripts", "deploy.sh"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(root, "scripts", "deploy.sh")); string(b) != "#!/bin/sh\n" {
		t.Fatalf("content = %q", b)
	}
}

func TestSetSecretUsesStdin(t *testing.T) {
	fr := &FakeRunner{}
	env := &Env{Root: t.TempDir(), Run: fr, In: Outputs{"server_ip": "1.2.3.4"}}
	step := SetSecret{Repo: "a/b", Name: "DEPLOY_HOST", FromOutput: "server_ip"}
	if step.ID() != "secret:DEPLOY_HOST" {
		t.Fatalf("ID = %q", step.ID())
	}
	if d := step.Describe(); d != "gh secret set DEPLOY_HOST --repo a/b" || strings.Contains(d, "1.2.3.4") {
		t.Fatalf("Describe = %q", d)
	}
	if _, err := step.Apply(context.Background(), env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fr.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(fr.Calls))
	}
	c := fr.Calls[0]
	if got := append([]string{c.Name}, c.Args...); !reflect.DeepEqual(got, []string{"gh", "secret", "set", "DEPLOY_HOST", "--repo", "a/b"}) {
		t.Fatalf("cmd = %v", got)
	}
	b, err := io.ReadAll(c.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "1.2.3.4" {
		t.Fatalf("stdin = %q, want 1.2.3.4", b)
	}
}

func TestSetSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(key, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, from := range map[string]string{"literal": key, "output": "$deploy_key_path"} {
		t.Run(name, func(t *testing.T) {
			fr := &FakeRunner{}
			env := &Env{Root: dir, Run: fr, In: Outputs{"deploy_key_path": key}}
			step := SetSecret{Repo: "a/b", Name: "DEPLOY_KEY", FromFile: from}
			if _, err := step.Apply(context.Background(), env); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			b, _ := io.ReadAll(fr.Calls[0].Stdin)
			if string(b) != "PRIVATE" {
				t.Fatalf("stdin = %q", b)
			}
		})
	}
}

func TestSetSecretMissingValue(t *testing.T) {
	fr := &FakeRunner{}
	step := SetSecret{Repo: "a/b", Name: "DEPLOY_HOST", FromOutput: "server_ip"}
	_, err := step.Apply(context.Background(), &Env{Run: fr, In: Outputs{}})
	if err == nil || !strings.Contains(err.Error(), "DEPLOY_HOST") {
		t.Fatalf("err = %v, want error naming DEPLOY_HOST", err)
	}
	if len(fr.Calls) != 0 {
		t.Fatalf("gh called without a value")
	}
}

func TestRunCommandParsesOutputs(t *testing.T) {
	fr := &FakeRunner{Responses: map[string][]byte{"tofu output": []byte("9.9.9.9")}}
	step := RunCommand{
		StepID: "infra:output",
		Label:  "read outputs",
		Cmd:    Cmd{Dir: "infra", Name: "tofu", Args: []string{"output", "-raw", "ip"}, Env: []string{"TOKEN=s3cret"}, Stdin: strings.NewReader("hidden")},
		Parse:  func(b []byte) (Outputs, error) { return Outputs{"server_ip": string(b)}, nil },
	}
	if step.ID() != "infra:output" {
		t.Fatalf("ID = %q", step.ID())
	}
	d := step.Describe()
	if d != "read outputs: $ tofu output -raw ip" {
		t.Fatalf("Describe = %q", d)
	}
	if strings.Contains(d, "s3cret") || strings.Contains(d, "hidden") {
		t.Fatalf("Describe leaks env/stdin: %q", d)
	}
	out, err := step.Apply(context.Background(), &Env{Run: fr})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out["server_ip"] != "9.9.9.9" {
		t.Fatalf("outputs = %v", out)
	}
	if (RunCommand{Cmd: Cmd{Name: "git", Args: []string{"init"}}}).Describe() != "$ git init" {
		t.Fatalf("unlabelled Describe wrong")
	}
}

func TestRunCommandNilParseAndErrors(t *testing.T) {
	boom := errors.New("boom")
	fr := &FakeRunner{Errors: map[string]error{"gh repo": boom}}
	out, err := RunCommand{StepID: "x", Cmd: Cmd{Name: "git", Args: []string{"init"}}}.Apply(context.Background(), &Env{Run: fr})
	if err != nil || out != nil {
		t.Fatalf("nil parse: out=%v err=%v", out, err)
	}
	_, err = RunCommand{StepID: "y", Cmd: Cmd{Name: "gh", Args: []string{"repo", "view"}}}.Apply(context.Background(), &Env{Run: fr})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestExecRunner(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "cat; printf %s \"$VAUBAN_T\""}, Env: []string{"VAUBAN_T=ok"}, Stdin: strings.NewReader("in-")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "in-ok" {
		t.Fatalf("out = %q", out)
	}
	_, err = ExecRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo bad >&2; exit 3"}})
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v, want stderr included", err)
	}
}

func TestExecRunnerRedactsSecrets(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `echo "$TOK" >&2; exit 1`}, Env: []string{"TOK=s3cr3t-value"}})
	if err == nil || strings.Contains(err.Error(), "s3cr3t-value") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("env: err = %v, want redacted", err)
	}
	_, err = ExecRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "cat >&2; exit 1"}, Stdin: strings.NewReader("stdin-secret\n")})
	if err == nil || strings.Contains(err.Error(), "stdin-secret") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("stdin: err = %v, want redacted", err)
	}
}
