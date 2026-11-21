package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// openMailbox loads the config, opens the existing database (nothing to do
// without a previous `quietbox init`, and it never creates one) and then
// connects. The database comes first so a missing one fails before any dial.
func openMailbox(cmd *cobra.Command, opts *options) (*config.Config, *store.Store, mail.Backend, error) {
	cfg, err := opts.loadConfig(false)
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := openState(cmd.Context(), cfg, false)
	if err != nil {
		return nil, nil, nil, err
	}
	b, err := imapbackend.Dial(cmd.Context(), imapbackend.Options{
		Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS,
		Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
	})
	if err != nil {
		_ = st.Close()
		return nil, nil, nil, err
	}
	return cfg, st, b, nil
}

func newOnceCmd(opts *options) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "once",
		Short: "File the mail that arrived since the last run, then exit (for cron)",
		Long: `Looks at INBOX mail that arrived since the last run (or since 'quietbox init'),
moves what the classifier files elsewhere, and exits. Every move is written to the
audit log before it happens and can be reversed with 'quietbox undo'. Mail you have
already read or flagged is left alone (see [run] in the config).

With --dry-run it prints what it would do and changes nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, st, b, err := openMailbox(cmd, opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			defer func() { _ = b.Close() }()

			po := engine.ProcessOptions{DryRun: dryRun}
			if opts.verbose {
				po.Progress = func(msg string) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg) }
			}
			rep, err := engine.Process(cmd.Context(), cfg, b, st, po)
			if rep != nil {
				if werr := writeProcessReport(cmd.OutOrStdout(), rep, dryRun); werr != nil && err == nil {
					err = werr
				}
			}
			if err != nil {
				return err
			}
			return processReportError(rep)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would move; change nothing")
	return cmd
}

// processReportError makes the exit status non-zero when something needs
// attention, so cron mails it: failed moves, or a warning such as a folder the
// server renumbered.
func processReportError(rep *engine.ProcessReport) error {
	switch {
	case rep.Failed > 0:
		return fmt.Errorf("%d move(s) failed (see the audit log)", rep.Failed)
	case len(rep.Warnings) > 0:
		return fmt.Errorf("finished with %d warning(s)", len(rep.Warnings))
	}
	return nil
}

// writeProcessReport prints what a pass did. It lists senders and rules, never
// subjects or bodies.
func writeProcessReport(w io.Writer, rep *engine.ProcessReport, dryRun bool) error {
	if dryRun {
		_, _ = fmt.Fprintln(w, "dry run: nothing was changed")
	}
	if len(rep.Moves) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SENDER\tTO\tRULE\tSTATUS\tWHY")
		for _, m := range rep.Moves {
			status := m.Status
			if status == engine.MoveWouldMove {
				status = "would move"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Sender, m.To, m.Rule, status, m.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w)
	}
	verb := "moved"
	if dryRun {
		verb = "would move"
	}
	_, _ = fmt.Fprintf(w, "%d new, %d %s, %d stayed in INBOX, %d skipped (read, flagged or already moved once), %d failed\n",
		rep.Fetched, len(rep.Moves)-rep.Failed, verb, rep.Stayed, rep.Skipped, rep.Failed)
	if n := rep.TrainedSenders(); n > 0 {
		for _, t := range rep.Trained {
			_, _ = fmt.Fprintln(w, t.String())
		}
		verb := "trained"
		if dryRun {
			verb = "would train"
		}
		_, _ = fmt.Fprintf(w, "%s %d sender(s) from your moves\n", verb, n)
	}
	for _, warn := range rep.Warnings {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warn)
	}
	return nil
}
