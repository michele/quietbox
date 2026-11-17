package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
)

func newCheckCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Connect, list server capabilities and show which configured folders exist (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(opts.configPath)
			if err != nil {
				return err
			}
			if opts.verbose {
				fmt.Fprintf(cmd.ErrOrStderr(), "connecting to %s:%d as %s (tls=%s)\n",
					cfg.IMAP.Host, cfg.IMAP.Port, cfg.IMAP.Username, cfg.IMAP.TLS)
			}
			b, err := imapbackend.Dial(cmd.Context(), imapbackend.Options{
				Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS,
				Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
			})
			if err != nil {
				return err
			}
			defer b.Close()
			report, err := engine.Check(cmd.Context(), cfg, b)
			if err != nil {
				return err
			}
			report.Write(cmd.OutOrStdout())
			return nil
		},
	}
}
