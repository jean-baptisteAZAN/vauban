package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/remote"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// addTargetFlags adds the flags that choose and describe an existing server.
func addTargetFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("target", "", "where it runs: cloud (a new VM) or existing (your own server)")
	f.String("host", "", "existing server: IP address or hostname")
	f.Int("ssh-port", 0, "existing server: SSH port (default 22)")
	f.String("ssh-user", "", "existing server: SSH user")
	f.String("ssh-key", "", `existing server: private key path, or "agent"`)
}

// applyTargetFlags applies --target and the server flags to c . Switching to existing clears cloud/region/size,
// switching to cloud clears the server block, unless passed explicitly.
func applyTargetFlags(cmd *cobra.Command, c *config.Config) error {
	f := cmd.Flags()
	serverFlags := []string{"host", "ssh-port", "ssh-user", "ssh-key"}
	if f.Changed("target") {
		target, _ := f.GetString("target")
		switch target {
		case config.TargetCloud:
			c.Target = ""
		case config.TargetExisting:
			c.Target = target
		default:
			return &ExitError{Code: 2, Err: fmt.Errorf("--target %q: want cloud or existing", target)}
		}
	}
	changedServer := slices.ContainsFunc(serverFlags, f.Changed)
	if changedServer && !c.Existing() {
		return &ExitError{Code: 2, Err: errors.New("--host, --ssh-port, --ssh-user and --ssh-key need --target existing")}
	}
	if c.Existing() {
		if !f.Changed("cloud") {
			c.Cloud = ""
		}
		if !f.Changed("region") {
			c.Region = ""
		}
		if !f.Changed("size") {
			c.Size = ""
		}
		if changedServer {
			s := config.Server{}
			if c.Server != nil {
				s = *c.Server
			}
			if f.Changed("host") {
				s.Host, _ = f.GetString("host")
			}
			if f.Changed("ssh-port") {
				s.Port, _ = f.GetInt("ssh-port")
			}
			if f.Changed("ssh-user") {
				s.User, _ = f.GetString("ssh-user")
			}
			if f.Changed("ssh-key") {
				s.Key, _ = f.GetString("ssh-key")
			}
			c.Server = &s
		}
	} else {
		c.Server = nil
	}
	return nil
}

// missingExistingFlags reports the flags a non-interactive existing target
// still needs (exit 2).
func missingExistingFlags(c *config.Config) error {
	var missing []string
	s := c.Server
	if s == nil {
		s = &config.Server{}
	}
	for _, m := range []struct {
		flag string
		ok   bool
	}{{"--host", s.Host != ""}, {"--ssh-user", s.User != ""}, {"--ssh-key", s.Key != ""}, {"--domain", c.Domain != ""}} {
		if !m.ok {
			missing = append(missing, m.flag)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &ExitError{Code: 2, Err: errors.New("no vauban.yaml: --target existing also needs " + strings.Join(missing, ", ") + " (or run interactively)")}
}

// sshKeyChoices lists the private keys in ~/.ssh that have a matching .pub.
func sshKeyChoices() ([]ui.Choice, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ui.Choice
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".pub") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name+".pub")); err == nil {
			out = append(out, ui.Choice{ID: "~/.ssh/" + name})
		}
	}
	return out, nil
}

// writeAudit runs the audit once for plan: it prints the table, the Caddy
// guide when needed, and returns what must go under "before apply:" (the
// blockers, or the ssh error: plan still succeeds).
func writeAudit(ctx context.Context, w io.Writer, root, repo string, c *config.Config) []string {
	t, err := remote.TargetFor(root, c.Server)
	if err != nil {
		return []string{err.Error()}
	}
	a, err := remote.RunAudit(ctx, cmdRunner, t, c.Project, repo)
	if err != nil {
		return []string{"cannot reach " + t.User + "@" + t.Host + ": " + err.Error()}
	}
	ui.RenderAudit(w, a)
	if g := remote.CaddyGuide(a); a.Caddy == remote.CaddyUserNeedsSetup && g != "" {
		ui.Section(w, "caddy setup")
		fmt.Fprintln(w, g)
	}
	return a.Blockers()
}
