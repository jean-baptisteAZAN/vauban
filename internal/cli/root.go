// Package cli wires the vauban command tree.
package cli

import (
	"context"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"
)

// NewRoot builds the root command. Subcommands are added by callers via AddCommand.
func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "vauban",
		Short:         "Detect your repo and set up CI, containers, OpenTofu infra and deploy",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          runDoctor,
	}
	pf := root.PersistentFlags()
	pf.String("dir", ".", "project directory")
	pf.Bool("yes", false, "accept defaults without prompting")
	pf.Bool("force", false, "overwrite existing files")
	root.AddCommand(newVersionCmd(version), newPlanCmd())
	return root
}

// Execute runs the root command through fang for styled help and errors.
func Execute(ctx context.Context, version string) error {
	return fang.Execute(ctx, NewRoot(version), fang.WithVersion(version))
}

// ExitError makes the process exit with Code after fang has printed Err.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
