// Package cloud defines the cloud provider contract used to provision the VM
// (OpenTofu code generation, credentials, API lookups) and the registry of
// supported providers.
//
// It must not import internal/engine or internal/module: the engine's
// preflight depends on this package.
package cloud

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Option is a selectable value (region, size) with a human label.
type Option struct{ ID, Label string }

// TFInput is what a provider needs to render its OpenTofu configuration.
type TFInput struct {
	Name, Region, Size string
	SSHPublicKeys      []string // deploy key + author key
}

// Provider is a cloud the VM can be provisioned on.
type Provider interface {
	ID() string // "hetzner"
	DefaultRegion() string
	DefaultSize() string
	// CredentialsEnv returns env vars for tofu (e.g. HCLOUD_TOKEN=…), read from
	// the env or the cloud CLI config file; the error names what is missing.
	CredentialsEnv() ([]string, error)
	CheckAuth(ctx context.Context) error // cheap authenticated API call
	Regions(ctx context.Context) ([]Option, error)
	Sizes(ctx context.Context, region string) ([]Option, error)
	// Terraform renders the provider block, ssh key(s), server and firewall,
	// with outputs server_ip and server_ipv6.
	Terraform(in TFInput) ([]byte, error)
}

var providers = map[string]Provider{}

func register(p Provider) { providers[p.ID()] = p }

// Get returns the provider with the given id.
func Get(id string) (Provider, error) {
	p, ok := providers[id]
	if !ok {
		return nil, fmt.Errorf("unknown cloud %q", id)
	}
	return p, nil
}

// IDs returns the registered provider ids, sorted.
func IDs() []string {
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// sanitizeName lowercases name and keeps only [a-z0-9-], collapsing runs of
// other characters into a single '-' and trimming dashes at the ends. It
// returns "vauban" when nothing usable remains.
func sanitizeName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := b.String()
	if len(s) > 63 {
		s = s[:63]
	}
	s = strings.Trim(s, "-")
	if s == "" {
		return "vauban"
	}
	return s
}

// hclString quotes s as an HCL string literal, escaping template sequences.
func hclString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "${", "$${", "%{", "%%{")
	return `"` + r.Replace(s) + `"`
}
