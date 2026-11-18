// Package cloud defines the cloud provider contract used to provision the VM
// (OpenTofu code generation, credentials, API lookups) and the registry of
// supported providers.
//
// It must not import internal/engine or internal/module: the engine's
// preflight depends on this package.
package cloud

import (
	"context"
	"embed"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Option is a selectable value (region, size) with a human label.
type Option struct{ ID, Label string }

// TFInput is what a provider needs to render its OpenTofu configuration.
type TFInput struct {
	Name, Region, Size string
	SSHPublicKeys      []string // deploy key only; the author key is installed via cloud-init
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
	// Sizes lists the sizes orderable in region. Providers that can read
	// per-type quotas leave out the sizes the account cannot create.
	Sizes(ctx context.Context, region string) ([]Option, error)
	// Quota reports whether the account may create one more server of size
	// in region. Quotas that cannot be read are OK with a "not checked"
	// reason; errors are for failed API calls.
	Quota(ctx context.Context, region, size string) (QuotaStatus, error)
	// Terraform renders the provider block, ssh key(s), server and firewall,
	// with outputs server_ip and server_ipv6.
	Terraform(in TFInput) ([]byte, error)
}

// QuotaStatus is the result of a quota check. Reason is human-readable,
// never contains secrets, and says why when OK is false (or what was not
// checked when OK is true).
type QuotaStatus struct {
	OK     bool
	Reason string
}

var quotaURLs = map[string]string{
	"scaleway":     "https://console.scaleway.com/organization/quotas",
	"digitalocean": "https://cloud.digitalocean.com/account/team (Droplet limit → Increase)",
	"hetzner":      "https://console.hetzner.com (project → Limits → Request change)",
}

// QuotaURL is where quota or limit increases are requested for cloud id.
func QuotaURL(id string) string { return quotaURLs[id] }

// quotaFix is the advice that ends a quota message for a server size.
func quotaFix(id string) string {
	return "pick another size (vauban plan --size …) or request a quota increase at " + QuotaURL(id)
}

// QuotaHint is the advice printed when the cloud refused a server for quota
// reasons (e.g. tofu apply failing with "quota exceeded").
func QuotaHint(id, region, size string) string {
	return fmt.Sprintf("%s: the account quota for %s in %s looks exhausted — %s", id, size, region, quotaFix(id))
}

//go:embed tf/*
var tfTemplates embed.FS

var (
	providersMu sync.RWMutex
	providers   = map[string]Provider{}
)

func register(p Provider) {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers[p.ID()] = p
}

// Register installs p, replacing any provider with the same id, and returns a
// function that restores the previous state. It lets tests in other packages
// install a fake provider.
func Register(p Provider) (restore func()) {
	providersMu.Lock()
	prev, had := providers[p.ID()]
	providers[p.ID()] = p
	providersMu.Unlock()
	return func() {
		providersMu.Lock()
		defer providersMu.Unlock()
		if had {
			providers[p.ID()] = prev
		} else {
			delete(providers, p.ID())
		}
	}
}

// Get returns the provider with the given id.
func Get(id string) (Provider, error) {
	providersMu.RLock()
	defer providersMu.RUnlock()
	p, ok := providers[id]
	if !ok {
		return nil, fmt.Errorf("unknown cloud %q", id)
	}
	return p, nil
}

// IDs returns the registered provider ids, sorted.
func IDs() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
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
