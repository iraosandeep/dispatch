package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintf(a.Stdout, "dispatch %s (commit %s, built %s, %s %s/%s)\n",
				a.Build.Version, a.Build.Commit, a.Build.Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
