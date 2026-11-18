package cloud

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"text/template"

	"github.com/digitalocean/godo"
	"gopkg.in/yaml.v3"
)

// Defaults verified 2026-11-18: debian-13-x64 is listed on
// docs.digitalocean.com/products/droplets/details/images/, and s-1vcpu-1gb is
// the entry-level Basic droplet ($6/mo).
const (
	digitaloceanRegion          = "fra1"
	digitaloceanSize            = "s-1vcpu-1gb"
	digitaloceanImage           = "debian-13-x64"
	digitaloceanProviderVersion = "2.108" // digitalocean/digitalocean 2.108.0
)

var digitaloceanTmpl = template.Must(template.ParseFS(tfTemplates, "tf/digitalocean.tf.tmpl"))

func init() { register(&digitalocean{}) }

type digitalocean struct {
	endpoint string // API base URL override, used by tests
}

func (*digitalocean) ID() string            { return "digitalocean" }
func (*digitalocean) DefaultRegion() string { return digitaloceanRegion }
func (*digitalocean) DefaultSize() string   { return digitaloceanSize }

func (*digitalocean) CredentialsEnv() ([]string, error) {
	tok, err := digitaloceanToken()
	if err != nil {
		return nil, err
	}
	return []string{"DIGITALOCEAN_TOKEN=" + tok}, nil
}

var errNoDigitaloceanToken = errors.New(`digitalocean: no API token found: set DIGITALOCEAN_TOKEN or run "doctl auth init"`)

// digitaloceanToken reads DIGITALOCEAN_TOKEN (or DIGITALOCEAN_ACCESS_TOKEN),
// else access-token from the doctl config file. Errors never contain the token.
func digitaloceanToken() (string, error) {
	for _, k := range []string{"DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN"} {
		if tok := os.Getenv(k); tok != "" {
			return tok, nil
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errNoDigitaloceanToken
	}
	path := filepath.Join(dir, "doctl", "config.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoDigitaloceanToken
	}
	if err != nil {
		return "", fmt.Errorf("digitalocean: read %s: %w", path, err)
	}
	var cfg struct {
		AccessToken string `yaml:"access-token"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// yaml errors can quote file content, so do not wrap err.
		return "", fmt.Errorf("digitalocean: parse %s: invalid YAML", path)
	}
	if cfg.AccessToken == "" {
		return "", errNoDigitaloceanToken
	}
	return cfg.AccessToken, nil
}

func (d *digitalocean) client() (*godo.Client, error) {
	tok, err := digitaloceanToken()
	if err != nil {
		return nil, err
	}
	apiURL := "https://api.digitalocean.com/"
	opts := []godo.ClientOpt{godo.SetUserAgent("vauban")}
	if d.endpoint != "" {
		apiURL = d.endpoint
		opts = append(opts, godo.SetBaseURL(d.endpoint))
	}
	return godo.New(bearerClient(tok, apiURL), opts...)
}

func (d *digitalocean) CheckAuth(ctx context.Context) error {
	c, err := d.client()
	if err != nil {
		return err
	}
	if _, _, err := c.Account.Get(ctx); err != nil {
		return fmt.Errorf("digitalocean: authentication check failed: %w", err)
	}
	return nil
}

// Regions lists the regions currently accepting new droplets.
func (d *digitalocean) Regions(ctx context.Context) ([]Option, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	var out []Option
	opt := &godo.ListOptions{PerPage: 200}
	for {
		regions, resp, err := c.Regions.List(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: list regions: %w", err)
		}
		for _, r := range regions {
			if r.Available {
				out = append(out, Option{ID: r.Slug, Label: r.Slug + " · " + r.Name})
			}
		}
		if next, ok := nextPage(resp); ok {
			opt.Page = next
			continue
		}
		break
	}
	slices.SortFunc(out, func(a, b Option) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// Sizes lists droplet sizes available in region, cheapest first.
func (d *digitalocean) Sizes(ctx context.Context, region string) ([]Option, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	var sizes []godo.Size
	opt := &godo.ListOptions{PerPage: 200}
	for {
		page, resp, err := c.Sizes.List(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: list sizes: %w", err)
		}
		for _, s := range page {
			if s.Available && slices.Contains(s.Regions, region) {
				sizes = append(sizes, s)
			}
		}
		if next, ok := nextPage(resp); ok {
			opt.Page = next
			continue
		}
		break
	}
	slices.SortFunc(sizes, func(a, b godo.Size) int {
		return cmp.Or(cmp.Compare(a.PriceMonthly, b.PriceMonthly), cmp.Compare(a.Vcpus, b.Vcpus),
			cmp.Compare(a.Memory, b.Memory), cmp.Compare(a.Slug, b.Slug))
	})
	out := make([]Option, 0, len(sizes))
	for _, s := range sizes {
		label := fmt.Sprintf("%s · %d vCPU · %s GB", s.Slug, s.Vcpus, strconv.FormatFloat(float64(s.Memory)/1024, 'f', -1, 64))
		if s.PriceMonthly > 0 {
			price := fmt.Sprintf("%.2f", s.PriceMonthly)
			if s.PriceMonthly == float64(int(s.PriceMonthly)) {
				price = strconv.Itoa(int(s.PriceMonthly))
			}
			label += " · $" + price + "/mo"
		}
		out = append(out, Option{ID: s.Slug, Label: label})
	}
	return out, nil
}

// nextPage returns the next page number of a paginated godo response.
func nextPage(resp *godo.Response) (int, bool) {
	if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
		return 0, false
	}
	p, err := resp.Links.CurrentPage()
	if err != nil {
		return 0, false
	}
	return p + 1, true
}

func (*digitalocean) Terraform(in TFInput) ([]byte, error) {
	switch {
	case in.Region == "":
		return nil, errors.New("digitalocean: region is required")
	case in.Size == "":
		return nil, errors.New("digitalocean: size is required")
	case len(in.SSHPublicKeys) == 0:
		return nil, errors.New("digitalocean: at least one SSH public key is required")
	}
	keys := make([]string, len(in.SSHPublicKeys))
	for i, k := range in.SSHPublicKeys {
		keys[i] = hclString(k)
	}
	var buf bytes.Buffer
	err := digitaloceanTmpl.Execute(&buf, struct {
		Version, Name, Region, Size, Image string
		Keys                               []string
	}{digitaloceanProviderVersion, hclString(sanitizeName(in.Name)), hclString(in.Region), hclString(in.Size), hclString(digitaloceanImage), keys})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bearerTransport adds the API token to requests for the API host only, so it
// is never sent to another host (e.g. on a cross-host redirect).
type bearerTransport struct{ token, host string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == t.host {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// bearerClient returns an HTTP client that authenticates requests to apiURL.
func bearerClient(token, apiURL string) *http.Client {
	host := ""
	if u, err := url.Parse(apiURL); err == nil {
		host = u.Host
	}
	return &http.Client{Transport: bearerTransport{token, host}}
}

// Quota compares the account droplet_limit (GET /v2/account) with the
// number of droplets (GET /v2/droplets meta.total). The limit is account
// wide, so size and region do not matter. When replacing, the change adds no
// droplet (a resize is in place, and the generated tf has no
// create_before_destroy), so it fails only when the account is already over
// the limit.
func (d *digitalocean) Quota(ctx context.Context, _, _ string, replacing bool) (QuotaStatus, error) {
	c, err := d.client()
	if err != nil {
		return QuotaStatus{}, err
	}
	acct, _, err := c.Account.Get(ctx)
	if err != nil {
		return QuotaStatus{}, fmt.Errorf("digitalocean: read account: %w", err)
	}
	if acct.DropletLimit <= 0 {
		return QuotaStatus{OK: true, Reason: "not checked: no droplet limit reported"}, nil
	}
	_, resp, err := c.Droplets.List(ctx, &godo.ListOptions{PerPage: 1})
	if err != nil {
		return QuotaStatus{}, fmt.Errorf("digitalocean: count droplets: %w", err)
	}
	if resp == nil || resp.Meta == nil {
		return QuotaStatus{OK: true, Reason: "not checked: droplet count not reported"}, nil
	}
	used := resp.Meta.Total
	if used > acct.DropletLimit || (!replacing && used == acct.DropletLimit) {
		return QuotaStatus{Reason: fmt.Sprintf("digitalocean: droplet limit reached (%d of %d in use) — delete a droplet or request a higher limit at %s",
			used, acct.DropletLimit, QuotaURL("digitalocean"))}, nil
	}
	return QuotaStatus{OK: true}, nil
}
