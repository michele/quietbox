package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
)

type scanFlags struct {
	since string
	csv   string
	limit int
	all   bool
	now   string
}

func newScanCmd(opts *options) *cobra.Command {
	f := &scanFlags{}
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Show what quietbox would do with the mail in your INBOX (shadow mode, changes nothing)",
		Long: `Classifies INBOX mail with what 'quietbox init' learned and prints where each
message would go and why. It only reads: the mailbox is opened read-only, and
nothing is written to the database or the audit log. Reports contain sender
addresses but never subjects or message bodies.

By default only messages that would leave INBOX are listed (the newest 50);
the totals at the end always cover everything it scanned.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runScan(cmd, opts, f) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.since, "since", "", "how far back to look, e.g. 30d (default: learn.inbox_lookback)")
	fl.StringVar(&f.csv, "csv", "", "also write every row to this CSV file")
	fl.IntVar(&f.limit, "limit", 50, "list at most this many messages (0 = all)")
	fl.BoolVar(&f.all, "all", false, "also list messages that would stay in INBOX")
	fl.StringVar(&f.now, "now", "", "treat this date (YYYY-MM-DD) as today; for tests and demos")
	_ = fl.MarkHidden("now")
	return cmd
}

func runScan(cmd *cobra.Command, opts *options, f *scanFlags) error {
	so := engine.ScanOptions{
		Progress: func(msg string) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg) },
	}
	var err error
	if so.Now, err = parseNow(f.now); err != nil {
		return err
	}
	if f.since != "" {
		if so.Since, err = config.ParseDuration(f.since); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
	}
	if f.limit < 0 {
		return fmt.Errorf("--limit: must be 0 or more, got %d", f.limit)
	}

	cfg, err := opts.loadConfig(false)
	if err != nil {
		return err
	}
	// Check the database before dialing: there is nothing to scan against
	// without a previous `quietbox init`, and this never creates one.
	st, err := openState(cmd.Context(), cfg, false)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	b, err := imapbackend.Dial(cmd.Context(), imapbackend.Options{
		Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS,
		Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
	})
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()

	rep, err := engine.Scan(cmd.Context(), cfg, b, st, so)
	if err != nil {
		return err
	}
	if err := rep.Write(cmd.OutOrStdout(), f.limit, f.all); err != nil {
		return err
	}
	if f.csv != "" {
		if err := writeCSVFile(f.csv, rep.WriteCSV); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nCSV written to %s\n", f.csv)
	}
	return nil
}

// writeCSVFile writes a report with mode 0600: it lists the addresses you
// correspond with.
func writeCSVFile(path string, write func(io.Writer) error) (err error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	return write(out)
}
