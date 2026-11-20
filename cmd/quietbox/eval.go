package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
)

type evalFlags struct {
	cutoff  string
	history string
	csv     string
	now     string

	// experiments: not defaults, only for measuring what they would change
	mixedOverride bool
}

func (f *evalFlags) policy() classify.Policy {
	return classify.Policy{MixedOverride: f.mixedOverride}
}

// defaultEvalCutoff is 60 days before now, at the start of that day (UTC).
func defaultEvalCutoff(now time.Time) time.Time {
	d := now.UTC().AddDate(0, 0, -60)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

func newEvalCmd(opts *options) *cobra.Command {
	f := &evalFlags{}
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Measure how well quietbox would have filed your recent mail (hold-out evaluation, changes nothing)",
		Long: `Learns only from mail that arrived before a cutoff date, then classifies the mail
that arrived after it and compares the result with the folder that mail is
actually in (where SaneBox or you put it).

Senders quietbox knows from before the cutoff agree almost by construction, so
the report separates them from NEW senders. The new-sender agreement is the
number that measures the detectors. "Hidden important" counts mail that is in
your INBOX but quietbox would have filed away: the failure to watch.

It reads the mailbox and writes nothing: no database, no folders, no flags.

--mixed-override switches on an experiment so you can see what it would change
(it is not the default, and the report says when it was on). Compare runs with
the same --cutoff.
Reports contain sender addresses but never subjects or message bodies.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runEval(cmd, opts, f) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.cutoff, "cutoff", "", "learn from mail before this date (YYYY-MM-DD) and test on the rest (default: 60 days ago)")
	fl.StringVar(&f.history, "history", "", "only learn from this long before the cutoff, e.g. 1y (default: everything; INBOX is capped by learn.inbox_lookback)")
	fl.StringVar(&f.csv, "csv", "", "write the disagreements to this CSV file")
	fl.BoolVar(&f.mixedOverride, "mixed-override", false, "experiment: a strong detector match overrides a mixed sender's majority route (off by default)")
	fl.StringVar(&f.now, "now", "", "treat this date (YYYY-MM-DD) as today; for tests and demos")
	_ = fl.MarkHidden("now")
	return cmd
}

func runEval(cmd *cobra.Command, opts *options, f *evalFlags) error {
	now, err := parseNow(f.now)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	eo := engine.EvalOptions{
		Cutoff:   defaultEvalCutoff(now),
		Policy:   f.policy(),
		Progress: func(msg string) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg) },
	}
	if f.cutoff != "" {
		if eo.Cutoff, err = time.Parse("2006-01-02", f.cutoff); err != nil {
			return fmt.Errorf("--cutoff: want YYYY-MM-DD, got %q", f.cutoff)
		}
	}
	if f.history != "" {
		if eo.History, err = config.ParseDuration(f.history); err != nil {
			return fmt.Errorf("--history: %w", err)
		}
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	b, err := imapbackend.Dial(cmd.Context(), imapbackend.Options{
		Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS,
		Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
	})
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()

	rep, err := engine.Eval(cmd.Context(), cfg, b, eo)
	if err != nil {
		return err
	}
	if err := rep.Write(cmd.OutOrStdout()); err != nil {
		return err
	}
	if f.csv != "" {
		if err := writeCSVFile(f.csv, rep.WriteCSV); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nDisagreements written to %s\n", f.csv)
	}
	return nil
}
