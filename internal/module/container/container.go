// Package container generates the Dockerfile and .dockerignore for a repo.
//
// Spike (docker init): Docker 29.8.2 here ships no usable `docker init` (the
// subcommand is absent from `docker --help`; where Docker Desktop provides it,
// it is a fully interactive TUI with no flags or stdin mode and it writes
// compose files too), so it cannot be driven non-interactively or planned
// transparently. We therefore render embedded templates and ship no RunCommand
// path.
package container

import (
	"bytes"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"text/template"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// Defaults verified 2026-11-18 (Node LTS "Lithium", Python 3.15 latest stable,
// uv release, distroless static-debian13 and golang:1.27 tags exist).
const (
	defaultNode   = "26"
	defaultPython = "3.15"
	defaultGo     = "1.27"
	uvVersion     = "0.13.9"
)

//go:embed templates/*
var templates embed.FS

// Module implements engine.Module for the container setup.
type Module struct{}

// New returns the container module.
func New() *Module { return &Module{} }

func (*Module) ID() string         { return "container" }
func (*Module) Title() string      { return "container" }
func (*Module) Requires() []string { return nil }

func (*Module) Detect(p *detect.Project) engine.Status {
	if p.HasDockerfile {
		return engine.StatusPresent
	}
	return engine.StatusMissing
}

type data struct {
	Version, PM, Port, UV, Entry, Lock string
}

func (*Module) Plan(p *detect.Project, c *config.Config) ([]engine.Step, error) {
	d := data{PM: p.PackageManager, Port: itoa(c.AppPort), UV: uvVersion}
	var name string
	switch p.Stack {
	case "go":
		name, d.Version = "go", orDefault(p.LangVersion, defaultGo)
	case "node":
		name, d.Version = "node", orDefault(p.LangVersion, defaultNode)
		d.Lock = nodeLock(p.PackageManager)
	case "python":
		name, d.Version = "python", orDefault(p.LangVersion, defaultPython)
		d.Entry = "app.py"
		if !exists(p.Root, "app.py") && exists(p.Root, "main.py") {
			d.Entry = "main.py"
		}
		d.Lock = "requirements.txt"
		if !exists(p.Root, "requirements.txt") {
			d.Lock = ""
		}
	default:
		if p.HasDockerfile {
			return nil, nil
		}
		return nil, errors.New("unsupported stack: add a Dockerfile and rerun")
	}
	tpl, err := template.ParseFS(templates, "templates/"+name+".Dockerfile")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, d); err != nil {
		return nil, err
	}
	ignore, err := templates.ReadFile("templates/dockerignore")
	if err != nil {
		return nil, err
	}
	return []engine.Step{
		engine.WriteFile{Path: "Dockerfile", Content: buf.Bytes(), Mode: 0o644},
		engine.WriteFile{Path: ".dockerignore", Content: ignore, Mode: 0o644},
	}, nil
}

func nodeLock(pm string) string {
	switch pm {
	case "pnpm":
		return "pnpm-lock.yaml"
	case "yarn":
		return "yarn.lock"
	case "bun":
		return "bun.lock*"
	}
	return "package-lock.json*"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

func itoa(n int) string { return strconv.Itoa(n) }
