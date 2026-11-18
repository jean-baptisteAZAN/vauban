package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
)

// Choice is a selectable value with an optional human label.
type Choice struct{ ID, Label string }

// Sources feeds AskConfig with the clouds and, per cloud, regions, sizes and
// defaults. Regions and Sizes may call cloud APIs; on error AskConfig falls
// back to a text input prefilled with the default.
type Sources struct {
	Clouds        []Choice
	DefaultRegion func(cloud string) string
	DefaultSize   func(cloud string) string
	Regions       func(ctx context.Context, cloud string) ([]Choice, error)
	Sizes         func(ctx context.Context, cloud, region string) ([]Choice, error)
	DefaultPort   int
}

const lookupTimeout = 20 * time.Second

// AskConfig asks for cloud, region, size, domain and port, starting from
// existing (which may be nil), and returns the resulting config.
func AskConfig(p *detect.Project, existing *config.Config, src Sources) (*config.Config, error) {
	c := config.Config{}
	if existing != nil {
		c = *existing
	}
	if c.Project == "" {
		c.Project = p.Name
	}
	prevCloud := c.Cloud

	if c.Cloud == "" && len(src.Clouds) > 0 {
		c.Cloud = src.Clouds[0].ID
	}
	cloudOpts := make([]huh.Option[string], 0, len(src.Clouds))
	for _, ch := range src.Clouds {
		cloudOpts = append(cloudOpts, huh.NewOption(ch.Label, ch.ID))
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Cloud").Description("where the server runs").Options(cloudOpts...).Value(&c.Cloud),
	)).Run(); err != nil {
		return nil, err
	}
	if c.Cloud != prevCloud {
		c.Region, c.Size = "", ""
	}
	if c.Region == "" {
		c.Region = src.DefaultRegion(c.Cloud)
	}
	if c.Size == "" {
		c.Size = src.DefaultSize(c.Cloud)
	}

	regions, err := lookup("Fetching regions…", func(ctx context.Context) ([]Choice, error) {
		return src.Regions(ctx, c.Cloud)
	})
	if err := pick("Region", regions, err, &c.Region); err != nil {
		return nil, err
	}
	sizes, err := lookup("Fetching server sizes…", func(ctx context.Context) ([]Choice, error) {
		return src.Sizes(ctx, c.Cloud, c.Region)
	})
	if err := pick("Size", sizes, err, &c.Size); err != nil {
		return nil, err
	}

	if c.AppPort == 0 {
		c.AppPort = src.DefaultPort
	}
	port := strconv.Itoa(c.AppPort)
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Domain").Description("must point to the server; leave empty to use sslip.io").
			Placeholder("empty = <ip>.sslip.io").Value(&c.Domain),
		huh.NewInput().Title("App port").Description("the port your app listens on in the container").
			Value(&port).Validate(func(s string) error { _, err := ParsePort(s); return err }),
	)).Run(); err != nil {
		return nil, err
	}
	c.Domain = strings.TrimSpace(c.Domain)
	if c.AppPort, err = ParsePort(port); err != nil {
		return nil, err
	}
	return &c, nil
}

// ParsePort parses a TCP port in 1-65535.
func ParsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, errors.New("port must be a number between 1 and 65535")
	}
	return n, nil
}

// lookup runs fetch behind a spinner.
func lookup(title string, fetch func(context.Context) ([]Choice, error)) ([]Choice, error) {
	var out []Choice
	var ferr error
	err := spinner.New().Title(" " + title).ActionWithErr(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		defer cancel()
		out, ferr = fetch(ctx)
		return nil
	}).Run()
	if err != nil {
		return nil, err
	}
	return out, ferr
}

// pick asks for one of opts, or for free text prefilled with *value when the
// options could not be listed (the error is shown, shortened).
func pick(title string, opts []Choice, listErr error, value *string) error {
	if listErr != nil || len(opts) == 0 {
		why := "no options returned"
		if listErr != nil {
			why = shortError(listErr)
		}
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title(title).Description(fmt.Sprintf("could not list them (%s); type a value", why)).
				Value(value).Validate(huh.ValidateNotEmpty()),
		)).Run()
	}
	options := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		key := o.ID
		if o.Label != "" && o.Label != o.ID {
			key = o.ID + "  " + o.Label
		}
		options = append(options, huh.NewOption(key, o.ID))
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Options(options...).Height(min(len(options)+2, 12)).Value(value),
	)).Run()
}

// shortError keeps the first line of err, capped, for display in a form.
func shortError(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	if len(s) > 120 {
		s = strings.ToValidUTF8(s[:117], "") + "…"
	}
	return s
}
