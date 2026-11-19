// Package config reads and writes the vauban.yaml project file.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// FileName is the name of the config file at the repo root.
const FileName = "vauban.yaml"

var clouds = []string{"scaleway", "digitalocean", "hetzner"}

// Deployment targets.
const (
	TargetCloud    = "cloud"
	TargetExisting = "existing"
)

// Config is the content of vauban.yaml.
type Config struct {
	Project string  `yaml:"project"`
	Target  string  `yaml:"target,omitempty"` // cloud (default) | existing
	Cloud   string  `yaml:"cloud,omitempty"`  // scaleway | digitalocean | hetzner
	Region  string  `yaml:"region,omitempty"`
	Size    string  `yaml:"size,omitempty"`
	Domain  string  `yaml:"domain"` // "" → sslip.io derived after apply
	AppPort int     `yaml:"app_port"`
	Server  *Server `yaml:"server,omitempty"`
}

// Server is an existing machine reached over SSH (target: existing).
type Server struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port,omitempty"`
	User string `yaml:"user"`
	Key  string `yaml:"key"` // path (~ expanded) or the literal "agent"
}

// Existing reports whether the config targets an existing server.
func (c *Config) Existing() bool { return c.Target == TargetExisting }

// SSHPort returns the SSH port, 22 when unset.
func (s *Server) SSHPort() int {
	if s.Port == 0 {
		return 22
	}
	return s.Port
}

// KeyPath returns the private key path with a leading ~/ expanded, or ""
// when the key is the literal "agent".
func (s *Server) KeyPath() (string, error) {
	if s.Key == "agent" {
		return "", nil
	}
	if rest, ok := strings.CutPrefix(s.Key, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %s: %w", s.Key, err)
		}
		return filepath.Join(home, rest), nil
	}
	return s.Key, nil
}

// Load reads vauban.yaml from root. A missing file yields an error wrapping fs.ErrNotExist.
func Load(root string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", FileName, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return &c, nil
}

// Save writes the config to root/vauban.yaml.
func (c *Config) Save(root string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode %s: %w", FileName, err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", FileName, err)
	}
	return nil
}

// Validate reports every problem found in the config.
func (c *Config) Validate() error {
	var errs []error
	if c.Project == "" {
		errs = append(errs, errors.New("project is required"))
	}
	if c.AppPort < 1 || c.AppPort > 65535 {
		errs = append(errs, fmt.Errorf("app_port %d out of range 1-65535", c.AppPort))
	}
	if c.Domain != "" && !validHostname(c.Domain) {
		errs = append(errs, fmt.Errorf("domain %q is not a valid hostname (e.g. app.example.com)", c.Domain))
	}
	switch c.Target {
	case "", TargetCloud:
		errs = append(errs, c.validateCloud()...)
	case TargetExisting:
		errs = append(errs, c.validateExisting()...)
	default:
		errs = append(errs, fmt.Errorf("unsupported target %q (want cloud or existing)", c.Target))
	}
	return errors.Join(errs...)
}

func (c *Config) validateCloud() []error {
	for _, cl := range clouds {
		if c.Cloud == cl {
			return nil
		}
	}
	return []error{fmt.Errorf("unsupported cloud %q (want scaleway, digitalocean or hetzner)", c.Cloud)}
}

func (c *Config) validateExisting() []error {
	var errs []error
	if !existingProject.MatchString(c.Project) && c.Project != "" {
		errs = append(errs, fmt.Errorf("project %q must match [a-z0-9][a-z0-9-]* (max 63 chars) when target is existing", c.Project))
	}
	if c.Domain == "" {
		errs = append(errs, errors.New("domain is required when target is existing"))
	}
	for _, f := range []struct{ name, val string }{{"cloud", c.Cloud}, {"region", c.Region}, {"size", c.Size}} {
		if f.val != "" {
			errs = append(errs, fmt.Errorf("%s must be empty when target is existing", f.name))
		}
	}
	s := c.Server
	if s == nil {
		return append(errs, errors.New("server is required when target is existing"))
	}
	if s.Host == "" {
		errs = append(errs, errors.New("server.host is required"))
	} else if !validServerHost(s.Host) {
		errs = append(errs, fmt.Errorf("server.host %q is not a valid hostname or IP", s.Host))
	}
	if s.User == "" {
		errs = append(errs, errors.New("server.user is required"))
	} else if !validUser(s.User) {
		errs = append(errs, fmt.Errorf("server.user %q is not a valid unix user name", s.User))
	}
	if s.Port < 0 || s.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port %d out of range 0-65535", s.Port))
	}
	if s.Key == "" {
		errs = append(errs, errors.New("server.key is required (a key path or \"agent\")"))
	} else if !validKey(s.Key) {
		errs = append(errs, fmt.Errorf("server.key %q must not start with '-' or contain whitespace or control characters", s.Key))
	}
	return errs
}

var existingProject = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

var userName = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?$`)

// validUser reports whether u is a POSIX-style user name (max 32 chars); it
// can never start with '-' or contain '@', so it is safe in user@host.
func validUser(u string) bool { return len(u) <= 32 && userName.MatchString(u) }

// validKey reports whether k is the literal "agent" or a path that cannot be
// read as an ssh option or split by whitespace.
func validKey(k string) bool {
	if k == "agent" {
		return true
	}
	if strings.HasPrefix(k, "-") {
		return false
	}
	for _, r := range k {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// validServerHost accepts an IP address, a dotted hostname or a single-label
// host such as "myvps".
func validServerHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// validHostname reports whether h is an RFC 1123 hostname with at least
// one dot: labels of 1-63 letters, digits and inner hyphens, 253 bytes max.
func validHostname(h string) bool {
	labels := strings.Split(h, ".")
	if len(h) > 253 || len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// ValidHostname reports whether h is a valid public domain (the rule Validate
// applies to Domain), for form validation.
func ValidHostname(h string) bool { return validHostname(h) }

// ValidServerHost reports whether h is a valid SSH host (the rule Validate
// applies to server.host), for form validation.
func ValidServerHost(h string) bool { return validServerHost(h) }

// ValidUser reports whether u is a valid SSH user name (the rule Validate
// applies to server.user), for form validation.
func ValidUser(u string) bool { return validUser(u) }
