package cloud

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	account "github.com/scaleway/scaleway-sdk-go/api/account/v3"
	iam "github.com/scaleway/scaleway-sdk-go/api/iam/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"gopkg.in/yaml.v3"
)

// Defaults verified 2026-11-18 against the public Scaleway API
// (instance/v1 products/servers and marketplace/v2 local-images, fr-par-1):
// debian_trixie is a marketplace label (x86_64 images list PLAY2-PICO as
// compatible) and PLAY2-PICO (1 vCPU, 2 GB, x86_64) is the smallest x86
// type with "available" stock; STARDUST1-S is in "shortage" and the DEV1
// family is being retired.
const (
	scalewayZone            = "fr-par-1"
	scalewaySize            = "PLAY2-PICO"
	scalewayImage           = "debian_trixie"
	scalewayProviderVersion = "2.92" // scaleway/scaleway 2.92.1
	scalewayHoursPerMonth   = 720    // 30-day month, as the SDK documents monthly_price
)

var scalewayTmpl = template.Must(template.ParseFS(tfTemplates, "tf/scaleway.tf.tmpl"))

func init() { register(&scaleway{}) }

type scaleway struct {
	endpoint string // API base URL override, used by tests
}

func (*scaleway) ID() string            { return "scaleway" }
func (*scaleway) DefaultRegion() string { return scalewayZone }
func (*scaleway) DefaultSize() string   { return scalewaySize }

var errNoScalewayCreds = errors.New(`scaleway: no API credentials found: set SCW_ACCESS_KEY, SCW_SECRET_KEY and SCW_DEFAULT_PROJECT_ID or run "scw init"`)

type scalewayCreds struct{ access, secret, project, zone, org string }

// scalewayCredentials reads SCW_ACCESS_KEY, SCW_SECRET_KEY and
// SCW_DEFAULT_PROJECT_ID (and SCW_DEFAULT_ZONE), filling whatever is missing
// from the active profile of the scw config file (SCW_CONFIG_PATH, SCW_PROFILE
// are honoured, parsed without the SDK loader). Errors never contain secret values.
func scalewayCredentials() (scalewayCreds, error) {
	c := scalewayCreds{
		access:  os.Getenv("SCW_ACCESS_KEY"),
		secret:  os.Getenv("SCW_SECRET_KEY"),
		project: os.Getenv("SCW_DEFAULT_PROJECT_ID"),
		zone:    os.Getenv("SCW_DEFAULT_ZONE"),
		org:     os.Getenv("SCW_DEFAULT_ORGANIZATION_ID"),
	}
	if c.access == "" || c.secret == "" || c.project == "" {
		prof, err := loadScalewayProfile()
		if err != nil {
			return c, err
		}
		c.access = cmp.Or(c.access, prof.AccessKey)
		c.secret = cmp.Or(c.secret, prof.SecretKey)
		c.project = cmp.Or(c.project, prof.DefaultProjectID)
		c.zone = cmp.Or(c.zone, prof.DefaultZone)
		c.org = cmp.Or(c.org, prof.DefaultOrganizationID)
	}
	var missing []string
	for _, m := range []struct{ name, val string }{
		{"SCW_ACCESS_KEY", c.access}, {"SCW_SECRET_KEY", c.secret}, {"SCW_DEFAULT_PROJECT_ID", c.project},
	} {
		if m.val == "" {
			missing = append(missing, m.name)
		}
	}
	if len(missing) == 3 {
		return c, errNoScalewayCreds
	}
	if len(missing) > 0 {
		return c, fmt.Errorf(`scaleway: missing %s: set SCW_ACCESS_KEY, SCW_SECRET_KEY and SCW_DEFAULT_PROJECT_ID or run "scw init"`, strings.Join(missing, ", "))
	}
	return c, nil
}

// scalewayStderr receives the config permission warning; tests replace it.
var scalewayStderr io.Writer = os.Stderr

type scalewayProfile struct {
	AccessKey        string `yaml:"access_key"`
	SecretKey        string `yaml:"secret_key"`
	DefaultProjectID string `yaml:"default_project_id"`
	DefaultZone      string `yaml:"default_zone"`
	// DefaultOrganizationID is optional: quotas fall back to the project's.
	DefaultOrganizationID string `yaml:"default_organization_id"`
}

// loadScalewayProfile reads the active profile of the scw config file. It
// parses the file itself instead of scw.LoadConfig, which prints a warning on
// stdout when the file is group/world readable. A missing file is not an error.
// Errors never contain file content.
func loadScalewayProfile() (scalewayProfile, error) {
	var none scalewayProfile
	path := scw.GetConfigPath() // computes the path only; honours SCW_CONFIG_PATH
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && os.Getenv("SCW_CONFIG_PATH") == "" && strings.HasSuffix(path, ".yaml") {
		// same fallback as the scw CLI: config.yml next to config.yaml
		path = strings.TrimSuffix(path, ".yaml") + ".yml"
		data, err = os.ReadFile(path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return none, nil
	}
	if err != nil {
		return none, fmt.Errorf("scaleway: cannot read %s", path)
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(scalewayStderr, "warning: %s is readable by others; run chmod 600 %s\n", path, path)
	}
	var cfg struct {
		scalewayProfile `yaml:",inline"`
		ActiveProfile   string                     `yaml:"active_profile"`
		Profiles        map[string]scalewayProfile `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// yaml errors can quote file content, so do not wrap err.
		return none, fmt.Errorf("scaleway: parse %s: invalid YAML", path)
	}
	prof := cfg.scalewayProfile
	name := cmp.Or(os.Getenv("SCW_PROFILE"), cfg.ActiveProfile)
	if name != "" && name != "default" {
		p, ok := cfg.Profiles[name]
		if !ok {
			return none, fmt.Errorf("scaleway: profile %q not found in %s", name, path)
		}
		// the selected profile overrides the default one field by field
		prof.AccessKey = cmp.Or(p.AccessKey, prof.AccessKey)
		prof.SecretKey = cmp.Or(p.SecretKey, prof.SecretKey)
		prof.DefaultProjectID = cmp.Or(p.DefaultProjectID, prof.DefaultProjectID)
		prof.DefaultZone = cmp.Or(p.DefaultZone, prof.DefaultZone)
		prof.DefaultOrganizationID = cmp.Or(p.DefaultOrganizationID, prof.DefaultOrganizationID)
	}
	return prof, nil
}

func (*scaleway) CredentialsEnv() ([]string, error) {
	c, err := scalewayCredentials()
	if err != nil {
		return nil, err
	}
	env := []string{"SCW_ACCESS_KEY=" + c.access, "SCW_SECRET_KEY=" + c.secret, "SCW_DEFAULT_PROJECT_ID=" + c.project}
	if c.zone != "" {
		env = append(env, "SCW_DEFAULT_ZONE="+c.zone)
	}
	return env, nil
}

func (s *scaleway) client() (*scw.Client, error) {
	c, err := scalewayCredentials()
	if err != nil {
		return nil, err
	}
	opts := []scw.ClientOption{scw.WithAuth(c.access, c.secret), scw.WithDefaultProjectID(c.project), scw.WithUserAgent("vauban")}
	if s.endpoint != "" {
		opts = append(opts, scw.WithAPIURL(s.endpoint))
	}
	client, err := scw.NewClient(opts...)
	if err != nil {
		// the SDK validation error quotes the offending key, so do not wrap err.
		return nil, errors.New("scaleway: invalid credentials format (expected an SCW access key, and UUID secret key and project ID)")
	}
	return client, nil
}

// CheckAuth lists servers (limit 1) in the default zone, which needs a valid key.
func (s *scaleway) CheckAuth(ctx context.Context) error {
	c, err := s.client()
	if err != nil {
		return err
	}
	_, err = instance.NewAPI(c).ListServers(&instance.ListServersRequest{Zone: scalewayZone, PerPage: scw.Uint32Ptr(1)}, scw.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("scaleway: authentication check failed: %w", err)
	}
	return nil
}

var scalewayZoneLabels = map[scw.Zone]string{
	scw.ZoneFrPar1: "Paris 1", scw.ZoneFrPar2: "Paris 2", scw.ZoneFrPar3: "Paris 3",
	scw.ZoneNlAms1: "Amsterdam 1", scw.ZoneNlAms2: "Amsterdam 2", scw.ZoneNlAms3: "Amsterdam 3",
	scw.ZonePlWaw1: "Warsaw 1", scw.ZonePlWaw2: "Warsaw 2", scw.ZonePlWaw3: "Warsaw 3",
	scw.ZoneItMil1: "Milan 1",
}

// Regions lists the zones the Instance API serves; a zone is the unit tofu
// provisions in, so it doubles as the region.
func (*scaleway) Regions(context.Context) ([]Option, error) {
	zones := (&instance.API{}).Zones()
	out := make([]Option, 0, len(zones))
	for _, z := range zones {
		label := string(z)
		if name := scalewayZoneLabels[z]; name != "" {
			label += " · " + name
		}
		out = append(out, Option{ID: string(z), Label: label})
	}
	return out, nil
}

// Sizes lists x86 instance types orderable in zone (not in shortage, not end
// of service, and, when the key can read quotas, a quota above 0), cheapest
// first.
func (s *scaleway) Sizes(ctx context.Context, zone string) ([]Option, error) {
	z, err := scw.ParseZone(zone)
	if err != nil {
		return nil, fmt.Errorf("scaleway: invalid zone %q", zone)
	}
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	api := instance.NewAPI(c)
	quotas, qerr := s.typeQuotas(ctx, c, z) // unreadable quotas filter nothing
	avail, err := api.GetServerTypesAvailability(&instance.GetServerTypesAvailabilityRequest{Zone: z}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("scaleway: list instance availability: %w", err)
	}
	types, err := api.ListServersTypes(&instance.ListServersTypesRequest{Zone: z}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("scaleway: list instance types: %w", err)
	}
	type row struct {
		id    string
		t     *instance.ServerType
		month float64
	}
	var rows []row
	zeroQuota := 0
	for id, t := range types.Servers {
		a, ok := avail.Servers[id]
		if t == nil || !ok || a == nil || a.Availability == instance.ServerTypesAvailabilityShortage ||
			t.EndOfService || t.Arch != instance.ArchX86_64 || (t.Gpu != nil && *t.Gpu > 0) {
			continue
		}
		if limit, ok := quotas[id]; qerr == nil && ok && limit == 0 {
			zeroQuota++
			continue
		}
		month := float64(t.HourlyPrice) * scalewayHoursPerMonth
		if t.MonthlyPrice != nil && *t.MonthlyPrice > 0 {
			month = float64(*t.MonthlyPrice)
		}
		rows = append(rows, row{id, t, month})
	}
	if len(rows) == 0 && zeroQuota > 0 {
		// kept short: the size form shows the first 120 characters
		return nil, fmt.Errorf("scaleway: quota 0 for every type in %s; raise it at %s", zone, QuotaURL("scaleway"))
	}
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(a.month, b.month), cmp.Compare(a.t.Ncpus, b.t.Ncpus),
			cmp.Compare(a.t.RAM, b.t.RAM), cmp.Compare(a.id, b.id))
	})
	out := make([]Option, 0, len(rows))
	for _, r := range rows {
		label := fmt.Sprintf("%s · %d vCPU · %s GB", r.id, r.t.Ncpus, strconv.FormatFloat(float64(r.t.RAM)/(1<<30), 'f', -1, 64))
		if r.month > 0 {
			label += fmt.Sprintf(" · €%.2f/mo", r.month)
		}
		out = append(out, Option{ID: r.id, Label: label})
	}
	return out, nil
}

// scalewayQuotaPrefix names an instance type's quota: STARDUST1-S is
// limited by cp_servers_type_STARDUST1_S, per zone (the name the Instance
// API quotes when it refuses a server, and that IAM ListQuota returns).
const scalewayQuotaPrefix = "cp_servers_type_"

// errQuotaUnreadable means the quotas could not be read with this key
// (missing permission, unknown organization): nothing is filtered or failed.
var errQuotaUnreadable = errors.New("cannot read the organization quotas with this API key")

// typeQuotas returns the instance type quota limits of the organization in
// zone, by type ("STARDUST1-S" → 0). Types without a limit (unlimited or not
// listed) are absent. It reads IAM ListQuota (GET /iam/v1alpha1/quota),
// finding the organization from SCW_DEFAULT_ORGANIZATION_ID, the scw profile,
// or the project (GET /account/v3/projects/{id}).
func (s *scaleway) typeQuotas(ctx context.Context, c *scw.Client, zone scw.Zone) (map[string]uint64, error) {
	creds, err := scalewayCredentials()
	if err != nil {
		return nil, err
	}
	org := creds.org
	if org == "" {
		proj, err := account.NewProjectAPI(c).GetProject(&account.ProjectAPIGetProjectRequest{ProjectID: creds.project}, scw.WithContext(ctx))
		if err != nil {
			return nil, scalewayQuotaErr("read project", err)
		}
		org = proj.OrganizationID
	}
	if org == "" {
		return nil, errQuotaUnreadable
	}
	resp, err := iam.NewAPI(c).ListQuota(&iam.ListQuotaRequest{OrganizationID: org, PageSize: scw.Uint32Ptr(100)}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, scalewayQuotaErr("list quotas", err)
	}
	out := map[string]uint64{}
	for _, q := range resp.Quota {
		if q == nil {
			continue
		}
		name, ok := strings.CutPrefix(q.Name, scalewayQuotaPrefix)
		if !ok {
			continue
		}
		typ := strings.ReplaceAll(name, "_", "-")
		// The zone entry wins, even when unlimited (Limit nil); the global
		// entry applies only when there is no zone entry.
		var zoneLimit, globalLimit *iam.QuotumLimit
		for _, l := range q.Limits {
			switch {
			case l == nil:
			case l.Zone != nil && *l.Zone == zone:
				zoneLimit = l
			case l.Global != nil && *l.Global:
				globalLimit = l
			}
		}
		if lim := cmp.Or(zoneLimit, globalLimit); lim != nil && lim.Limit != nil {
			out[typ] = *lim.Limit
		}
	}
	return out, nil
}

// scalewayQuotaErr turns a denied or missing quota call into
// errQuotaUnreadable and wraps any other error.
func scalewayQuotaErr(what string, err error) error {
	var (
		resp   *scw.ResponseError
		denied *scw.PermissionsDeniedError
		auth   *scw.DeniedAuthenticationError
		gone   *scw.ResourceNotFoundError
	)
	switch {
	case errors.As(err, &denied), errors.As(err, &auth), errors.As(err, &gone):
		return errQuotaUnreadable
	case errors.As(err, &resp) && (resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404):
		return errQuotaUnreadable
	}
	return fmt.Errorf("scaleway: %s: %w", what, err)
}

// Quota checks the organization's quota for size in zone (IAM ListQuota)
// against the servers of that type already there (Instance dashboard,
// GET /instance/v1/zones/{zone}/dashboard). The quota is per type and zone,
// so replacing does not change the check: a server of another type or zone
// does not count against it.
func (s *scaleway) Quota(ctx context.Context, zone, size string, _ bool) (QuotaStatus, error) {
	z, err := scw.ParseZone(zone)
	if err != nil {
		return QuotaStatus{}, fmt.Errorf("scaleway: invalid zone %q", zone)
	}
	c, err := s.client()
	if err != nil {
		return QuotaStatus{}, err
	}
	quotas, err := s.typeQuotas(ctx, c, z)
	if errors.Is(err, errQuotaUnreadable) {
		return QuotaStatus{OK: true, Reason: "not checked: " + err.Error()}, nil
	}
	if err != nil {
		return QuotaStatus{}, err
	}
	limit, ok := quotas[size]
	switch {
	case !ok:
		return QuotaStatus{OK: true}, nil
	case limit == 0:
		return QuotaStatus{Reason: fmt.Sprintf("scaleway: quota for %s in %s is 0 — %s", size, zone, quotaFix("scaleway"))}, nil
	}
	dash, err := instance.NewAPI(c).GetDashboard(&instance.GetDashboardRequest{Zone: z}, scw.WithContext(ctx))
	if err != nil || dash.Dashboard == nil {
		return QuotaStatus{OK: true, Reason: "servers in use not checked"}, nil //nolint:nilerr // the limit is not 0: usage is best effort
	}
	if used := uint64(dash.Dashboard.ServersByTypes[size]); used >= limit {
		return QuotaStatus{Reason: fmt.Sprintf("scaleway: quota for %s in %s is %d and %d in use — %s", size, zone, limit, used, quotaFix("scaleway"))}, nil
	}
	return QuotaStatus{OK: true}, nil
}

var scalewayZoneRe = regexp.MustCompile(`^([a-z]{2}-[a-z]{3})-[0-9]+$`)

func (*scaleway) Terraform(in TFInput) ([]byte, error) {
	switch {
	case in.Region == "":
		return nil, errors.New("scaleway: region (zone) is required")
	case in.Size == "":
		return nil, errors.New("scaleway: size is required")
	}
	m := scalewayZoneRe.FindStringSubmatch(in.Region)
	if m == nil {
		return nil, fmt.Errorf("scaleway: invalid zone %q, expected e.g. fr-par-1", in.Region)
	}
	// in.SSHPublicKeys is not rendered: the deploy key is installed by
	// cloud-init (ssh_authorized_keys), never as a project-wide IAM key.
	var buf bytes.Buffer
	err := scalewayTmpl.Execute(&buf, struct {
		Version, Name, Zone, Region, Size, Image string
	}{scalewayProviderVersion, hclString(sanitizeName(in.Name)), hclString(in.Region), hclString(m[1]), hclString(in.Size), hclString(scalewayImage)})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
