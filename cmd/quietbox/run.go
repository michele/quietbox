package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

func newRunCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Keep INBOX filed: wait for new mail (IDLE) and file it as it arrives",
		Long: `Runs until interrupted (Ctrl-C or SIGTERM). Files the mail that arrived since the
last run, then waits for the server to report new mail and files that too. A lost
connection is retried with a growing delay. It refuses to start if the server cannot
MOVE, if 'quietbox init' has not run, or if another quietbox is running on this
mailbox. Every move is audited and can be reversed with 'quietbox undo'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(opts.configPath)
			if err != nil {
				return err
			}
			// Nothing to do without a previous init; fail before dialing.
			st, err := store.OpenExisting(cfg.State.Path)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			say := func(msg string) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg) }
			say("quietbox running; press Ctrl-C to stop")
			return engine.Run(ctx, cfg, st, engine.RunOptions{
				Connect: func(ctx context.Context) (mail.Backend, error) {
					return imapbackend.Dial(ctx, imapbackend.Options{
						Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS,
						Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
					})
				},
				Progress: say,
			})
		},
	}
}
