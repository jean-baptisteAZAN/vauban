package main

import (
	"context"
	"os"

	"github.com/jean-baptisteAZAN/vauban/internal/cli"
)

var version = "dev"

func main() {
	if err := cli.Execute(context.Background(), version); err != nil {
		os.Exit(1)
	}
}
