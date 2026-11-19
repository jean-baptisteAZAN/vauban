package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	c := tg.Cmd(false, "echo hi")
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
	want = []string{"-p", "2222", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + agent.KnownHosts,
		"--", "root@h.example", "sh", "-s", "--"}
	if got := agent.Cmd(true, "x").Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCmdQuotesArgs(t *testing.T) {
	c := target(t, "").Cmd(false, "x", "it's $(x)", "plain")
	n := len(c.Args)
	if c.Args[n-2] != `'it'\''s $(x)'` || c.Args[n-1] != `'plain'` {
		t.Fatalf("got %q", c.Args[n-2:])
	}
}

func TestPinnedEmptyFile(t *testing.T) {
	tg := target(t, "")
	f := &engine.FakeRunner{Responses: map[string][]byte{"ssh-keygen -F": []byte("h ssh-ed25519 AAAA\n")}}
	if Pinned(context.Background(), f, tg) {
		t.Fatal("missing file is not pinned")
	}
	_ = os.WriteFile(tg.KnownHosts, nil, 0o600)
	if Pinned(context.Background(), f, tg) {
		t.Fatal("empty file is not pinned")
	}
	if len(f.Calls) != 0 {
		t.Fatalf("ssh-keygen ran on an empty file: %v", f.Calls)
	}
}

// Pinning is per host and port: a key recorded for one server must not make
// ssh refuse another (StrictHostKeyChecking=yes) after a move.
func TestPinnedPerHost(t *testing.T) {
	tg := target(t, "")
	if err := os.WriteFile(tg.KnownHosts, []byte("other ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found := "[h.example]:2222 ssh-ed25519 AAAA\n"
	f := &engine.FakeRunner{Responses: map[string][]byte{
		"ssh-keygen -F [h.example]:2222 -f " + tg.KnownHosts: []byte(found),
	}}
	if !Pinned(context.Background(), f, tg) {
		t.Fatal("want pinned for [h.example]:2222")
	}
	other := tg
	other.Host = "new.example"
	if Pinned(context.Background(), f, other) {
		t.Fatal("a key for another host must not pin new.example")
	}
	// ssh-keygen -F exits 1 when nothing matches.
	f = &engine.FakeRunner{Errors: map[string]error{"ssh-keygen -F": errors.New("exit 1")}}
	if Pinned(context.Background(), f, tg) {
		t.Fatal("ssh-keygen failure means not pinned")
	}
	// Port 22 is recorded without brackets; ssh lowercases host names.
	p22 := tg
	p22.Host, p22.Port = "H.Example", 22
	f = &engine.FakeRunner{}
	Pinned(context.Background(), f, p22)
	if got := strings.Join(f.Calls[0].Args, " "); got != "-F h.example -f "+tg.KnownHosts {
		t.Fatalf("args %q", got)
	}
}

func TestExecUsesPerHostPolicy(t *testing.T) {
	tg := target(t, "")
	if err := os.WriteFile(tg.KnownHosts, []byte("other ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &engine.FakeRunner{Errors: map[string]error{"ssh-keygen -F": errors.New("exit 1")}}
	if _, err := Exec(context.Background(), f, tg, "true"); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[len(f.Calls)-1]
	if last.Name != "ssh" || !slices.Contains(last.Args, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("want accept-new for an unknown host, got %q", last.Args)
	}
	f = &engine.FakeRunner{Responses: map[string][]byte{"ssh-keygen -F": []byte("[h.example]:2222 ssh-ed25519 AAAA\n")}}
	if _, err := Exec(context.Background(), f, tg, "true"); err != nil {
		t.Fatal(err)
	}
	if last := f.Calls[len(f.Calls)-1]; !slices.Contains(last.Args, "StrictHostKeyChecking=yes") {
		t.Fatalf("want yes for a pinned host, got %q", last.Args)
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
