// Command quietbox is a self-hosted mail triage tool. This package only parses
// flags and calls the engine; it never touches the mailbox directly.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

type options struct {
	configPath string
	configSet  bool // --config was given explicitly
	account    string
	verbose    bool
	stderr     io.Writer // where config warnings go; set from the command
}

func defaultConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "quietbox", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "quietbox", "config.toml")
}

func newRootCmd() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "quietbox",
		Short:         "Move non-urgent mail out of your inbox, learning from how you file it",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       versionString(),
	}
	root.SetVersionTemplate("quietbox {{.Version}}\n")
	root.PersistentFlags().StringVar(&opts.configPath, "config", defaultConfigPath(), "path to the TOML config file")
	root.PersistentFlags().StringVar(&opts.account, "account", "", "use <config folder>/NAME.toml, with its own database (see `quietbox accounts`)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		opts.configSet = cmd.Flags().Changed("config")
		opts.stderr = cmd.ErrOrStderr()
		return nil
	}
	root.PersistentFlags().BoolVarP(&opts.verbose, "verbose", "v", false, "print progress to stderr (never credentials)")
	root.AddCommand(newCheckCmd(opts), newInitCmd(opts), newSendersCmd(opts), newScanCmd(opts), newEvalCmd(opts), newOnceCmd(opts), newRunCmd(opts), newUndoCmd(opts), newAccountsCmd(opts), newExplainCmd(opts), newStatsCmd(opts), newVersionCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
