// Package module registers the vauban modules (ci, container, infra, deploy).
package module

import "github.com/jean-baptisteAZAN/vauban/internal/engine"

// All returns every registered module. Later tasks append theirs here.
func All() []engine.Module { return nil }
