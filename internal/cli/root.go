// Package cli wires the vauban command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"charm.land/fang/v2"
	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// cmdRunner runs the external commands of apply and destroy. Tests replace it.
var cmdRunner engine.Runner = engine.ExecRunner{}

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
	pf.Bool("force", false, "overwrite existing generated files (never approves destroying resources)")
	root.AddCommand(newDoctorCmd(), newVersionCmd(version), newPlanCmd(), newApplyCmd(), newDestroyCmd(), newAuditCmd())
	return root
}

// Execute runs the root command through fang for styled help and errors.
func Execute(ctx context.Context, version string) error {
	return fang.Execute(ctx, NewRoot(version), fang.WithVersion(version), fang.WithErrorHandler(errorHandler))
}

// ExitError makes the process exit with Code after fang has printed Err.
// A nil Err exits quietly.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}
func (e *ExitError) Unwrap() error { return e.Err }

// errorHandler prints err the fang way, except for quiet exits.
func errorHandler(w io.Writer, styles fang.Styles, err error) {
	var exit *ExitError
	if errors.As(err, &exit) && exit.Err == nil {
		return
	}
	fang.DefaultErrorHandler(w, styles, err)
}

// abortable turns a form the user aborted (Ctrl-C, Esc) into a quiet exit 130.
func abortable(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		err := run(cmd, args)
		if errors.Is(err, huh.ErrUserAborted) {
			return &ExitError{Code: 130}
		}
		return err
	}
}
