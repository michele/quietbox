// Command quietbox is a self-hosted mail triage tool. This package only parses
// flags and calls the engine; it never touches the mailbox directly.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

type options struct {
	configPath string
	verbose    bool
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
	}
	root.PersistentFlags().StringVar(&opts.configPath, "config", defaultConfigPath(), "path to the TOML config file")
	root.PersistentFlags().BoolVarP(&opts.verbose, "verbose", "v", false, "print progress to stderr (never credentials)")
	root.AddCommand(newCheckCmd(opts), newInitCmd(opts), newSendersCmd(opts), newScanCmd(opts), newEvalCmd(opts), newOnceCmd(opts), newRunCmd(opts), newUndoCmd(opts))
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
