package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
)

func newAccountsCmd(_ *options) *cobra.Command {
	return &cobra.Command{
		Use:   "accounts",
		Short: "List the accounts in the config folder (use one with --account NAME)",
		Long: `Lists the *.toml files in the config folder: each one is an account you can
select with --account NAME. Every account has its own database and its two lock
files (<database>.lock and <database>.lock.run) and is run by its own quietbox
process. Nothing connects to a mailbox.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := config.ListAccounts(os.Getenv)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				_, _ = fmt.Fprintf(out, "No accounts found in %s.\nCreate %s/NAME.toml (see examples/) and select it with --account NAME.\n",
					config.ConfigDir(os.Getenv), config.ConfigDir(os.Getenv))
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tHOST\tUSERNAME\tDATABASE")
			for _, a := range list {
				if a.Err != nil {
					_, _ = fmt.Fprintf(tw, "%s\t(error: %v)\t\t\n", a.Name, a.Err)
					continue
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, a.Host, a.Username, a.StatePath)
			}
			return tw.Flush()
		},
	}
}
