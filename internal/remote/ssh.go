// Package remote drives an existing server over the system ssh binary, with
// the host key pinned in <root>/.vauban/known_hosts.
package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// ErrHostKeyChanged means ssh refused the server because its host key no
// longer matches the pinned one.
var ErrHostKeyChanged = errors.New("host key changed")

// Target is an SSH destination.
type Target struct {
	Host       string
	Port       int
	User       string
	Key        string // private key path, "" for the ssh agent
	KnownHosts string // <root>/.vauban/known_hosts
}

// TargetFor builds a Target from the server block, creating <root>/.vauban.
func TargetFor(root string, s *config.Server) (Target, error) {
	key, err := s.KeyPath()
	if err != nil {
		return Target{}, err
	}
	dir := filepath.Join(root, ".vauban")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Target{}, err
	}
	return Target{Host: s.Host, Port: s.SSHPort(), User: s.User, Key: key,
		KnownHosts: filepath.Join(dir, "known_hosts")}, nil
}

// As returns a copy of t connecting as another user and key.
func (t Target) As(user, key string) Target {
	t.User, t.Key = user, key
	return t
}

// Pinned reports whether a host key has been recorded.
func Pinned(t Target) bool {
	fi, err := os.Stat(t.KnownHosts)
	return err == nil && fi.Size() > 0
}

// Cmd builds the ssh command running script (on stdin) with sh -s and args,
// each POSIX single-quoted because ssh joins remote args into one shell string.
func (t Target) Cmd(script string, args ...string) engine.Cmd {
	policy := "accept-new"
	if Pinned(t) {
		policy = "yes"
	}
	a := []string{"-p", strconv.Itoa(t.Port),
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=" + policy,
		"-o", "UserKnownHostsFile=" + t.KnownHosts}
	if t.Key != "" {
		a = append(a, "-i", t.Key, "-o", "IdentitiesOnly=yes")
	}
	a = append(a, "--", t.User+"@"+t.Host, "sh", "-s", "--")
	for _, arg := range args {
		a = append(a, quote(arg))
	}
	return engine.Cmd{Name: "ssh", Args: a, Stdin: strings.NewReader(script)}
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Exec runs script on the target and returns its stdout.
func Exec(ctx context.Context, r engine.Runner, t Target, script string, args ...string) ([]byte, error) {
	out, err := r.Run(ctx, t.Cmd(script, args...))
	if err != nil && strings.Contains(err.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		return out, fmt.Errorf("%w for %s; if expected, remove the line from .vauban/known_hosts: %w",
			ErrHostKeyChanged, t.Host, err)
	}
	return out, err
}
