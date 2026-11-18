package cloud

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
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

type scalewayCreds struct{ access, secret, project, zone string }

// scalewayCredentials reads SCW_ACCESS_KEY, SCW_SECRET_KEY and
// SCW_DEFAULT_PROJECT_ID (and SCW_DEFAULT_ZONE), filling whatever is missing
// from the active profile of the scw config file (SCW_CONFIG_PATH, SCW_PROFILE
// are honoured). Errors never contain secret values.
func scalewayCredentials() (scalewayCreds, error) {
	c := scalewayCreds{
		access:  os.Getenv("SCW_ACCESS_KEY"),
		secret:  os.Getenv("SCW_SECRET_KEY"),
		project: os.Getenv("SCW_DEFAULT_PROJECT_ID"),
		zone:    os.Getenv("SCW_DEFAULT_ZONE"),
	}
	if c.access == "" || c.secret == "" || c.project == "" {
		path := scw.GetConfigPath()
		cfg, err := scw.LoadConfig()
		var notFound *scw.ConfigFileNotFoundError
		switch {
		case errors.As(err, &notFound):
			// no config file: only the env counts
		case err != nil:
			// the SDK error can quote file content, so do not wrap err.
			return c, fmt.Errorf("scaleway: cannot read %s: invalid or unreadable config", path)
		default:
			prof, err := cfg.GetActiveProfile()
			if err != nil {
				return c, fmt.Errorf("scaleway: active profile of %s not found", path)
			}
			c.access = cmp.Or(c.access, deref(prof.AccessKey))
			c.secret = cmp.Or(c.secret, deref(prof.SecretKey))
			c.project = cmp.Or(c.project, deref(prof.DefaultProjectID))
			c.zone = cmp.Or(c.zone, deref(prof.DefaultZone))
		}
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
// of service), cheapest first.
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
	for id, t := range types.Servers {
		a, ok := avail.Servers[id]
		if t == nil || !ok || a == nil || a.Availability == instance.ServerTypesAvailabilityShortage ||
			t.EndOfService || t.Arch != instance.ArchX86_64 || (t.Gpu != nil && *t.Gpu > 0) {
			continue
		}
		month := float64(t.HourlyPrice) * scalewayHoursPerMonth
		if t.MonthlyPrice != nil && *t.MonthlyPrice > 0 {
			month = float64(*t.MonthlyPrice)
		}
		rows = append(rows, row{id, t, month})
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

var scalewayZoneRe = regexp.MustCompile(`^([a-z]{2}-[a-z]{3})-[0-9]+$`)

func (*scaleway) Terraform(in TFInput) ([]byte, error) {
	switch {
	case in.Region == "":
		return nil, errors.New("scaleway: region (zone) is required")
	case in.Size == "":
		return nil, errors.New("scaleway: size is required")
	case len(in.SSHPublicKeys) == 0:
		return nil, errors.New("scaleway: at least one SSH public key is required")
	}
	m := scalewayZoneRe.FindStringSubmatch(in.Region)
	if m == nil {
		return nil, fmt.Errorf("scaleway: invalid zone %q, expected e.g. fr-par-1", in.Region)
	}
	keys := make([]string, len(in.SSHPublicKeys))
	for i, k := range in.SSHPublicKeys {
		keys[i] = hclString(k)
	}
	var buf bytes.Buffer
	err := scalewayTmpl.Execute(&buf, struct {
		Version, Name, Zone, Region, Size, Image string
		Keys                                     []string
	}{scalewayProviderVersion, hclString(sanitizeName(in.Name)), hclString(in.Region), hclString(m[1]), hclString(in.Size), hclString(scalewayImage), keys})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
