//go:build demo

// Command vauban-demo is the vauban CLI used to record docs/demo.gif. It is
// the real CLI with one change: every cloud provider's API calls (credential
// check, region and size listing) return canned answers instead of calling
// the cloud, so the recording never touches a real cloud. Everything
// else (detection, plan, files, OpenTofu code, cloud-init) is vauban's own
// code; docs/demo/setup.sh puts fake tofu, gh, docker and ssh-keygen on PATH.
//
// Build: go build -tags demo -o <dir>/vauban ./docs/demo/vauban-demo
package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/cli"
	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
)

// stub wraps a real provider (defaults, credentials lookup, Terraform
// rendering) and replaces its network calls with canned answers.
type stub struct {
	cloud.Provider
	regions, sizes []cloud.Option
}

func (stub) CheckAuth(context.Context) error { return nil }

func (s stub) Regions(context.Context) ([]cloud.Option, error) {
	time.Sleep(600 * time.Millisecond) // let the spinner show
	return s.regions, nil
}

func (s stub) Sizes(context.Context, string) ([]cloud.Option, error) {
	time.Sleep(600 * time.Millisecond)
	return s.sizes, nil
}

// canned lists, in each provider's label format (prices left out)
var canned = map[string][2][]cloud.Option{
	"digitalocean": {
		{{ID: "ams3", Label: "ams3 · Amsterdam 3"}, {ID: "fra1", Label: "fra1 · Frankfurt 1"}, {ID: "lon1", Label: "lon1 · London 1"}, {ID: "nyc3", Label: "nyc3 · New York 3"}},
		{{ID: "s-1vcpu-1gb", Label: "s-1vcpu-1gb · 1 vCPU · 1 GB"}, {ID: "s-1vcpu-2gb", Label: "s-1vcpu-2gb · 1 vCPU · 2 GB"}, {ID: "s-2vcpu-2gb", Label: "s-2vcpu-2gb · 2 vCPU · 2 GB"}},
	},
	"hetzner": {
		{{ID: "fsn1", Label: "fsn1 · Falkenstein, DE"}, {ID: "hel1", Label: "hel1 · Helsinki, FI"}, {ID: "nbg1", Label: "nbg1 · Nuremberg, DE"}},
		{{ID: "cx23", Label: "cx23 · 2 vCPU · 4 GB"}, {ID: "cx33", Label: "cx33 · 4 vCPU · 8 GB"}},
	},
	"scaleway": {
		{{ID: "fr-par-1", Label: "fr-par-1"}, {ID: "nl-ams-1", Label: "nl-ams-1"}},
		{{ID: "PLAY2-PICO", Label: "PLAY2-PICO · 1 vCPU · 2 GB"}},
	},
}

func main() {
	for id, lists := range canned {
		real, err := cloud.Get(id)
		if err != nil {
			panic(err)
		}
		cloud.Register(stub{Provider: real, regions: lists[0], sizes: lists[1]})
	}
	if err := cli.Execute(context.Background(), "demo"); err != nil {
		var exit *cli.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}
