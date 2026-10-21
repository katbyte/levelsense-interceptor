// Package cli implements the levelsense-interceptor command line interface: the cobra commands, flag and config handling, and the interceptor they
// run.
package cli

import (
	"fmt"

	"github.com/katbyte/go-kt/version"
	"github.com/spf13/cobra"
)

func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   toolName + " [command]",
		Short: toolName + " sits between a Level Sense freezer alarm and the Level Sense cloud, and reads the freezer's reports on the way through",
		Long: `Sits between a Level Sense freezer alarm and the Level Sense cloud. The freezer sends its
reports over plain HTTP to cloud.level-sense.com; with that name pointed here by the network's
DNS, each report is read, passed on to the real cloud unchanged, and the cloud's answer handed
back to the freezer as it was given. The app and its alerts go on working as before.
What the reports say is served for Prometheus at /metrics and for Home Assistant at /state.
Complete documentation is available at https://github.com/katbyte/levelsense-interceptor`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run \""+toolName+" help\" for more information about available "+toolName+" commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of " + toolName,
		Long:          `Print the version number of ` + toolName,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), toolName+" "+version.Version)
		},
	})

	root.AddCommand(serveCmd())

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}
	// the settings file is read once the flags are parsed, since a flag can name it; before any command's own checks, which read what it holds
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return loadConfig() }

	return root, nil
}
