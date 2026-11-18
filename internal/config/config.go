// Package config reads and writes the vauban.yaml project file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the name of the config file at the repo root.
const FileName = "vauban.yaml"

var clouds = []string{"scaleway", "digitalocean", "hetzner"}

// Config is the content of vauban.yaml.
type Config struct {
	Project string `yaml:"project"`
	Cloud   string `yaml:"cloud"` // scaleway | digitalocean | hetzner
	Region  string `yaml:"region"`
	Size    string `yaml:"size"`
	Domain  string `yaml:"domain"` // "" → sslip.io derived after apply
	AppPort int    `yaml:"app_port"`
}

// Load reads vauban.yaml from root. A missing file yields an error wrapping fs.ErrNotExist.
func Load(root string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", FileName, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return &c, nil
}

// Save writes the config to root/vauban.yaml.
func (c *Config) Save(root string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode %s: %w", FileName, err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", FileName, err)
	}
	return nil
}

// Validate reports every problem found in the config.
func (c *Config) Validate() error {
	var errs []error
	if c.Project == "" {
		errs = append(errs, errors.New("project is required"))
	}
	supported := false
	for _, cl := range clouds {
		if c.Cloud == cl {
			supported = true
		}
	}
	if !supported {
		errs = append(errs, fmt.Errorf("unsupported cloud %q (want scaleway, digitalocean or hetzner)", c.Cloud))
	}
	if c.AppPort < 1 || c.AppPort > 65535 {
		errs = append(errs, fmt.Errorf("app_port %d out of range 1-65535", c.AppPort))
	}
	return errors.Join(errs...)
}
