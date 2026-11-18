package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module"
	"github.com/jean-baptisteAZAN/vauban/internal/module/deploy"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// interactive reports whether prompts are possible (stdin and stdout TTYs).
var interactive = func() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

var cloudLabels = map[string]string{"scaleway": "Scaleway", "digitalocean": "DigitalOcean", "hetzner": "Hetzner"}

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Choose a cloud and show every file and command apply would run",
		Long: "Choose a cloud and show every file and command apply would run.\n\n" +
			"Uses vauban.yaml when it exists (--reconfigure asks again); flags override vauban.yaml.",
		Args: cobra.NoArgs,
		RunE: runPlan,
	}
	f := cmd.Flags()
	f.String("cloud", "", "cloud provider: scaleway, digitalocean or hetzner")
	f.String("region", "", "region (default: the provider's)")
	f.String("size", "", "server size (default: the provider's)")
	f.String("domain", "", "public domain (default: <ip>.sslip.io)")
	f.Int("port", 0, "port the app listens on (default: from Dockerfile or stack)")
	f.Bool("reconfigure", false, "ask again even if vauban.yaml exists")
	return cmd
}

func runPlan(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	force, _ := cmd.Flags().GetBool("force")
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	c, err := resolveConfig(cmd, p)
	if err != nil {
		return err
	}
	pl, err := preparePlan(root, c, force)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	writeConfigSummary(w, p, c)
	ui.RenderPlan(w, pl.items(root, force))
	writeDrift(w, pl.st, c, pl.infraStale)
	if err := engine.Preflight(pl.p, c, pl.mods); err != nil {
		ui.Warnings(w, "before apply:", strings.Split(err.Error(), "\n"))
	}
	ui.PlanFooter(w)
	return nil
}

// resolveConfig returns vauban.yaml, or builds it from flags or prompts, and
// saves it. Explicit flags override values from an existing vauban.yaml.
func resolveConfig(cmd *cobra.Command, p *detect.Project) (*config.Config, error) {
	f := cmd.Flags()
	reconfigure, _ := f.GetBool("reconfigure")
	yes, _ := f.GetBool("yes")
	existing, err := config.Load(p.Root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	c := &config.Config{}
	if existing != nil {
		*c = *existing
	}
	for name, dst := range map[string]*string{"cloud": &c.Cloud, "region": &c.Region, "size": &c.Size, "domain": &c.Domain} {
		if f.Changed(name) {
			*dst, _ = f.GetString(name)
		}
	}
	if f.Changed("port") {
		c.AppPort, _ = f.GetInt("port")
	}

	if existing == nil || reconfigure {
		if interactive() && !yes {
			if c, err = ui.AskConfig(p, c, formSources(p)); err != nil {
				return nil, err
			}
		} else if c.Cloud == "" {
			return nil, &ExitError{Code: 2, Err: errors.New("no vauban.yaml: pass --cloud or run interactively")}
		}
	}

	if c.Project == "" {
		c.Project = p.Name
	}
	if c.AppPort == 0 {
		c.AppPort = defaultPort(p)
	}
	if prov, err := cloud.Get(c.Cloud); err == nil {
		c.Region = orDefault(c.Region, prov.DefaultRegion())
		c.Size = orDefault(c.Size, prov.DefaultSize())
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", config.FileName, err)
	}
	if err := c.Save(p.Root); err != nil {
		return nil, err
	}
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// defaultPort is the Dockerfile's EXPOSE, else the stack's usual port.
func defaultPort(p *detect.Project) int {
	if p.ExposedPort > 0 {
		return p.ExposedPort
	}
	switch p.Stack {
	case "node":
		return 3000
	case "python":
		return 8000
	}
	return 8080
}

// formSources exposes the cloud registry to the config form.
func formSources(p *detect.Project) ui.Sources {
	var clouds []ui.Choice
	for _, id := range cloud.IDs() {
		clouds = append(clouds, ui.Choice{ID: id, Label: orDefault(cloudLabels[id], id)})
	}
	def := func(get func(cloud.Provider) string) func(string) string {
		return func(id string) string {
			if prov, err := cloud.Get(id); err == nil {
				return get(prov)
			}
			return ""
		}
	}
	choices := func(opts []cloud.Option, err error) ([]ui.Choice, error) {
		out := make([]ui.Choice, 0, len(opts))
		for _, o := range opts {
			out = append(out, ui.Choice{ID: o.ID, Label: o.Label})
		}
		return out, err
	}
	return ui.Sources{
		Clouds:        clouds,
		DefaultRegion: def(cloud.Provider.DefaultRegion),
		DefaultSize:   def(cloud.Provider.DefaultSize),
		Regions: func(ctx context.Context, id string) ([]ui.Choice, error) {
			prov, err := cloud.Get(id)
			if err != nil {
				return nil, err
			}
			return choices(prov.Regions(ctx))
		},
		Sizes: func(ctx context.Context, id, region string) ([]ui.Choice, error) {
			prov, err := cloud.Get(id)
			if err != nil {
				return nil, err
			}
			return choices(prov.Sizes(ctx, region))
		},
		DefaultPort: defaultPort(p),
	}
}

// moduleSteps is one module's planned steps.
type moduleSteps struct {
	id    string
	steps []engine.Step
}

// planned is everything plan and apply need: the ordered steps per module
// and the state with stale steps already cleared (in memory only).
type planned struct {
	p          *detect.Project
	mods       []engine.Module
	groups     []moduleSteps
	st         *engine.State
	infraStale []string
}

// buildPlan scans root and returns the ordered steps of every module and the
// loaded state, with stale step IDs (and, with force, file steps) removed
// from st.Done so they run again. It never saves state.
func buildPlan(_ context.Context, root string, c *config.Config, force bool) ([]engine.Step, *engine.State, error) {
	pl, err := preparePlan(root, c, force)
	if err != nil {
		return nil, nil, err
	}
	return pl.steps(), pl.st, nil
}

func preparePlan(root string, c *config.Config, force bool) (*planned, error) {
	p, err := detect.Scan(root)
	if err != nil {
		return nil, err
	}
	mods, err := engine.Order(module.All())
	if err != nil {
		return nil, err
	}
	pl := &planned{p: p, mods: mods}
	for _, m := range mods {
		steps, err := m.Plan(p, c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.ID(), err)
		}
		pl.groups = append(pl.groups, moduleSteps{id: m.ID(), steps: steps})
	}
	if pl.st, err = engine.LoadState(root); err != nil {
		return nil, err
	}
	pl.infraStale = infra.StaleSteps(pl.st, c)
	for _, id := range append(pl.infraStale, deploy.StaleSteps(pl.st, c)...) {
		delete(pl.st.Done, id)
	}
	if force {
		for id := range pl.st.Done {
			if strings.HasPrefix(id, "file:") || id == "infra:main-tf" || id == "infra:cloud-init" {
				delete(pl.st.Done, id)
			}
		}
	}
	return pl, nil
}

func (pl *planned) steps() []engine.Step {
	var out []engine.Step
	for _, g := range pl.groups {
		out = append(out, g.steps...)
	}
	return out
}

// items turns the steps into plan lines grouped by module.
func (pl *planned) items(root string, force bool) []ui.PlanGroup {
	rerender := map[string]bool{}
	for _, id := range pl.infraStale {
		rerender[id] = true
	}
	groups := make([]ui.PlanGroup, 0, len(pl.groups))
	for _, g := range pl.groups {
		pg := ui.PlanGroup{Module: g.id}
		for _, s := range g.steps {
			pg.Items = append(pg.Items, planItem(s, root, pl.st, force || rerender[s.ID()]))
		}
		groups = append(groups, pg)
	}
	return groups
}

// planItem classifies a step: done, secret, file (write or skip), command,
// or other. Files are recognised by a "write <path>" description.
func planItem(s engine.Step, root string, st *engine.State, overwrite bool) ui.PlanItem {
	d := s.Describe()
	if st.Done[s.ID()] {
		return ui.PlanItem{Kind: ui.PlanDone, Text: d}
	}
	if _, ok := s.(engine.SetSecret); ok {
		return ui.PlanItem{Kind: ui.PlanSecret, Text: d}
	}
	if path, ok := strings.CutPrefix(d, "write "); ok {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil && !overwrite {
			return ui.PlanItem{Kind: ui.PlanSkip, Text: path}
		}
		return ui.PlanItem{Kind: ui.PlanWrite, Text: path}
	}
	if label, line, ok := strings.Cut(d, "$ "); ok {
		return ui.PlanItem{Kind: ui.PlanCommand, Text: line, Note: strings.TrimSuffix(strings.TrimSpace(label), ":")}
	}
	return ui.PlanItem{Kind: ui.PlanOther, Text: d}
}

func writeConfigSummary(w io.Writer, p *detect.Project, c *config.Config) {
	ui.Heading(w, p.Name)
	ui.Row(w, "cloud", orDefault(cloudLabels[c.Cloud], c.Cloud)+" · "+c.Region+" · "+c.Size)
	ui.Row(w, "domain", orDefault(c.Domain, "<ip>.sslip.io"+ui.Hint(" (after apply)")))
	ui.Row(w, "port", fmt.Sprint(c.AppPort))
}

// writeDrift reports that vauban.yaml no longer matches the applied infra.
// The exact tofu plan is only meaningful once main.tf is re-rendered, so
// apply shows it.
func writeDrift(w io.Writer, st *engine.State, c *config.Config, stale []string) {
	if len(stale) == 0 {
		return
	}
	applied := orDefault(st.Outputs["infra_fingerprint"], "not applied")
	ui.Drift(w, "infra config changed: "+applied+" → "+infraFingerprint(c))
}

// infraFingerprint is cloud/region/size with provider defaults filled in,
// as the infra module records it.
func infraFingerprint(c *config.Config) string {
	region, size := c.Region, c.Size
	if prov, err := cloud.Get(c.Cloud); err == nil {
		region, size = orDefault(region, prov.DefaultRegion()), orDefault(size, prov.DefaultSize())
	}
	return c.Cloud + "/" + region + "/" + size
}
