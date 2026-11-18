package main

import (
	"context"
	"errors"
	"os"
	"runtime/debug"

	"github.com/jean-baptisteAZAN/vauban/internal/cli"
)

// version is set by goreleaser (-X main.version). Builds from `go install`
// leave it as "dev" and fall back to the module version.
var version = "dev"

// resolveVersion keeps an injected version and otherwise uses the module
// version recorded by the Go toolchain, when it is a real one.
func resolveVersion(injected, module string) string {
	if injected != "dev" || module == "" || module == "(devel)" {
		return injected
	}
	return module
}

func main() {
	v := version
	if bi, ok := debug.ReadBuildInfo(); ok {
		v = resolveVersion(version, bi.Main.Version)
	}
	if err := cli.Execute(context.Background(), v); err != nil {
		var exit *cli.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}
