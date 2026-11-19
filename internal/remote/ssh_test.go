package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

func target(t *testing.T, key string) Target {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vauban"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Target{Host: "h.example", Port: 2222, User: "root", Key: key,
		KnownHosts: filepath.Join(root, ".vauban", "known_hosts")}
}

func TestCmdArgs(t *testing.T) {
	tg := target(t, "/k/id")
	c := tg.Cmd("echo hi")
	want := []string{"-p", "2222", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + tg.KnownHosts,
		"-i", "/k/id", "-o", "IdentitiesOnly=yes", "--", "root@h.example", "sh", "-s", "--"}
	if c.Name != "ssh" || !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("got %s %q\nwant %q", c.Name, c.Args, want)
	}
	b, _ := io.ReadAll(c.Stdin)
	if string(b) != "echo hi" {
		t.Fatalf("stdin %q", b)
	}

	agent := target(t, "")
	if err := os.WriteFile(agent.KnownHosts, []byte("h ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Pinned(agent) {
		t.Fatal("want pinned")
	}
	want = []string{"-p", "2222", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + agent.KnownHosts,
		"--", "root@h.example", "sh", "-s", "--"}
	if got := agent.Cmd("x").Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCmdQuotesArgs(t *testing.T) {
	c := target(t, "").Cmd("x", "it's $(x)", "plain")
	n := len(c.Args)
	if c.Args[n-2] != `'it'\''s $(x)'` || c.Args[n-1] != `'plain'` {
		t.Fatalf("got %q", c.Args[n-2:])
	}
}

func TestPinnedEmptyFile(t *testing.T) {
	tg := target(t, "")
	if Pinned(tg) {
		t.Fatal("missing file is not pinned")
	}
	_ = os.WriteFile(tg.KnownHosts, nil, 0o600)
	if Pinned(tg) {
		t.Fatal("empty file is not pinned")
	}
}

func TestExecHostKeyChanged(t *testing.T) {
	tg := target(t, "")
	f := &engine.FakeRunner{Errors: map[string]error{
		"ssh -p": errors.New("ssh: exit 255: WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!"),
	}}
	_, err := Exec(context.Background(), f, tg, "true")
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "h.example") || !strings.Contains(err.Error(), ".vauban/known_hosts") {
		t.Fatalf("message %q", err)
	}
}

func TestExecOutputAndOtherError(t *testing.T) {
	tg := target(t, "")
	f := &engine.FakeRunner{Responses: map[string][]byte{"ssh -p": []byte("ok")}}
	out, err := Exec(context.Background(), f, tg, "true", "a")
	if err != nil || string(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	boom := errors.New("boom")
	f = &engine.FakeRunner{Errors: map[string]error{"ssh -p": boom}}
	if _, err := Exec(context.Background(), f, tg, "true"); !errors.Is(err, boom) || errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("got %v", err)
	}
}

func TestAs(t *testing.T) {
	tg := target(t, "/k/a").As("deploy", "/k/b")
	if tg.User != "deploy" || tg.Key != "/k/b" || tg.Host != "h.example" {
		t.Fatalf("%+v", tg)
	}
}

func TestTargetFor(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	tg, err := TargetFor(root, &config.Server{Host: "h", User: "u", Key: "~/id"})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, ".vauban"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v", fi, err)
	}
	if tg.Port != 22 || tg.Key != filepath.Join(home, "id") || tg.KnownHosts != filepath.Join(root, ".vauban", "known_hosts") {
		t.Fatalf("%+v", tg)
	}
	tg, err = TargetFor(root, &config.Server{Host: "h", User: "u", Key: "agent"})
	if err != nil || tg.Key != "" {
		t.Fatalf("%+v %v", tg, err)
	}
}
