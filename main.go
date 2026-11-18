package main

import (
	"context"
	"errors"
	"os"

	"github.com/jean-baptisteAZAN/vauban/internal/cli"
)

var version = "dev"

func main() {
	if err := cli.Execute(context.Background(), version); err != nil {
		var exit *cli.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}
