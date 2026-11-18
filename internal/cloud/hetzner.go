package cloud

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"text/template"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/pelletier/go-toml/v2"
)

// Defaults verified 2026-11-18 against the Hetzner Cloud changelog: cx23 is
// the smallest cost-optimized shared x86 type (cx22 and older were retired on
// 1 January 2026), and debian-13 is the current Debian stable image name.
const (
	hetznerRegion          = "nbg1"
	hetznerSize            = "cx23"
	hetznerProviderVersion = "1.70" // hetznercloud/hcloud 1.70.2
)

var hetznerTmpl = template.Must(template.ParseFS(tfTemplates, "tf/hetzner.tf.tmpl"))

func init() { register(&hetzner{}) }

type hetzner struct {
	endpoint string // API endpoint override, used by tests
}

func (*hetzner) ID() string            { return "hetzner" }
func (*hetzner) DefaultRegion() string { return hetznerRegion }
func (*hetzner) DefaultSize() string   { return hetznerSize }

func (*hetzner) CredentialsEnv() ([]string, error) {
	tok, err := hetznerToken()
	if err != nil {
		return nil, err
	}
	return []string{"HCLOUD_TOKEN=" + tok}, nil
}

var errNoHetznerToken = errors.New(`hetzner: no API token found: set HCLOUD_TOKEN or run "hcloud context create <name>"`)

// hetznerToken reads HCLOUD_TOKEN, else the active context of the hcloud CLI
// config (~/.config/hcloud/cli.toml).
func hetznerToken() (string, error) {
	if tok := os.Getenv("HCLOUD_TOKEN"); tok != "" {
		return tok, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errNoHetznerToken
	}
	path := filepath.Join(home, ".config", "hcloud", "cli.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoHetznerToken
	}
	if err != nil {
		return "", fmt.Errorf("hetzner: read %s: %w", path, err)
	}
	var cfg struct {
		ActiveContext string `toml:"active_context"`
		Contexts      []struct {
			Name  string `toml:"name"`
			Token string `toml:"token"`
		} `toml:"contexts"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("hetzner: parse %s: %w", path, err)
	}
	for _, c := range cfg.Contexts {
		if cfg.ActiveContext != "" && c.Name == cfg.ActiveContext && c.Token != "" {
			return c.Token, nil
		}
	}
	return "", errNoHetznerToken
}

func (h *hetzner) client() (*hcloud.Client, error) {
	tok, err := hetznerToken()
	if err != nil {
		return nil, err
	}
	opts := []hcloud.ClientOption{hcloud.WithToken(tok), hcloud.WithApplication("vauban", "")}
	if h.endpoint != "" {
		opts = append(opts, hcloud.WithEndpoint(h.endpoint))
	}
	return hcloud.NewClient(opts...), nil
}

func (h *hetzner) CheckAuth(ctx context.Context) error {
	c, err := h.client()
	if err != nil {
		return err
	}
	if _, _, err := c.Location.List(ctx, hcloud.LocationListOpts{ListOpts: hcloud.ListOpts{PerPage: 1}}); err != nil {
		return fmt.Errorf("hetzner: authentication check failed: %w", err)
	}
	return nil
}

func (h *hetzner) Regions(ctx context.Context) ([]Option, error) {
	c, err := h.client()
	if err != nil {
		return nil, err
	}
	locs, err := c.Location.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("hetzner: list locations: %w", err)
	}
	out := make([]Option, 0, len(locs))
	for _, l := range locs {
		out = append(out, Option{ID: l.Name, Label: l.Name + " · " + l.City + ", " + l.Country})
	}
	slices.SortFunc(out, func(a, b Option) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// Sizes lists shared x86 server types orderable in region, cheapest first.
func (h *hetzner) Sizes(ctx context.Context, region string) ([]Option, error) {
	c, err := h.client()
	if err != nil {
		return nil, err
	}
	types, err := c.ServerType.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("hetzner: list server types: %w", err)
	}
	type size struct {
		st    *hcloud.ServerType
		price float64 // monthly net, -1 when unknown
	}
	var sizes []size
	for _, st := range types {
		if st.CPUType != hcloud.CPUTypeShared || st.Architecture != hcloud.ArchitectureX86 || !orderableIn(st, region) {
			continue
		}
		sizes = append(sizes, size{st, monthlyPrice(st, region)})
	}
	slices.SortFunc(sizes, func(a, b size) int {
		if (a.price < 0) != (b.price < 0) {
			if a.price < 0 {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(a.price, b.price), cmp.Compare(a.st.Cores, b.st.Cores),
			cmp.Compare(a.st.Memory, b.st.Memory), cmp.Compare(a.st.Name, b.st.Name))
	})
	out := make([]Option, 0, len(sizes))
	for _, s := range sizes {
		label := fmt.Sprintf("%s · %d vCPU · %s GB", s.st.Name, s.st.Cores, strconv.FormatFloat(float64(s.st.Memory), 'f', -1, 32))
		if s.price >= 0 {
			label += fmt.Sprintf(" · €%.2f/mo", s.price)
		}
		out = append(out, Option{ID: s.st.Name, Label: label})
	}
	return out, nil
}

func orderableIn(st *hcloud.ServerType, region string) bool {
	for _, l := range st.Locations {
		if l.Location != nil && l.Location.Name == region {
			return l.Available && !l.IsDeprecated()
		}
	}
	return false
}

// monthlyPrice returns the monthly net price (EUR, Hetzner's only currency)
// of st in region, or -1 when unknown.
func monthlyPrice(st *hcloud.ServerType, region string) float64 {
	for _, p := range st.Pricings {
		if p.Location != nil && p.Location.Name == region {
			if v, err := strconv.ParseFloat(p.Monthly.Net, 64); err == nil {
				return v
			}
		}
	}
	return -1
}

func (*hetzner) Terraform(in TFInput) ([]byte, error) {
	switch {
	case in.Region == "":
		return nil, errors.New("hetzner: region is required")
	case in.Size == "":
		return nil, errors.New("hetzner: size is required")
	case len(in.SSHPublicKeys) == 0:
		return nil, errors.New("hetzner: at least one SSH public key is required")
	}
	keys := make([]string, len(in.SSHPublicKeys))
	for i, k := range in.SSHPublicKeys {
		keys[i] = hclString(k)
	}
	var buf bytes.Buffer
	err := hetznerTmpl.Execute(&buf, struct {
		Version, Name, Region, Size string
		Keys                        []string
	}{hetznerProviderVersion, hclString(sanitizeName(in.Name)), hclString(in.Region), hclString(in.Size), keys})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Quota is not checked: the Hetzner Cloud API exposes no account or project
// limits (they are only shown in the console), and a refused server fails
// tofu apply with resource_limit_exceeded.
func (*hetzner) Quota(context.Context, string, string) (QuotaStatus, error) {
	return QuotaStatus{OK: true, Reason: "not checked: the Hetzner Cloud API does not expose project limits"}, nil
}
