// Package module registers the vauban modules (ci, container, infra, deploy).
package module

import (
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/container"
)

// All returns every registered module. Later tasks append theirs here.
func All() []engine.Module { return []engine.Module{container.New()} }
