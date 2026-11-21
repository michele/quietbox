package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
)

func newUndoCmd(opts *options) *cobra.Command {
	var since string
	var dryRun bool
	var lockWait string
	cmd := &cobra.Command{
		Use:   "undo --since <duration>",
		Short: "Move back what quietbox filed recently",
		Long: `Reverses the moves quietbox made within the given time (e.g. 1h, 2d), newest
first, by moving each message from the folder quietbox put it in back to where it
came from. Nothing is deleted. A message that is no longer where quietbox put it
(you moved it yourself) is left alone and reported. Restored messages are never
filed away again. While another quietbox works on the same mailbox (a 'run' pass,
'once', 'init') it waits for it, at most --lock-timeout, then fails. A dry run never
waits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if since == "" {
				return fmt.Errorf("--since is required, e.g. --since 1h")
			}
			d, err := config.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			timeout, err := parseLockTimeout(lockWait)
			if err != nil {
				return err
			}
			cfg, st, b, err := openMailbox(cmd, opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			defer func() { _ = b.Close() }()

			rep, err := engine.Undo(cmd.Context(), cfg, b, st, engine.UndoOptions{
				Since: d, DryRun: dryRun, LockTimeout: timeout, OnLockWait: lockWaitNotice(cmd),
			})
			if rep != nil {
				if werr := writeUndoReport(cmd.OutOrStdout(), rep, dryRun); werr != nil && err == nil {
					err = werr
				}
			}
			if err != nil {
				return err
			}
			return undoReportError(rep)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "undo moves made within this long ago, e.g. 1h, 2d (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be restored; change nothing")
	addLockTimeoutFlag(cmd, &lockWait)
	return cmd
}

func undoReportError(rep *engine.UndoReport) error {
	failed := 0
	for _, o := range rep.Outcomes {
		if o.Status == engine.UndoFailed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d message(s) could not be restored (see the audit log)", failed)
	}
	return nil
}

var undoStatusText = map[string]string{
	engine.UndoRestored:          "restored",
	engine.UndoWouldRestore:      "would restore",
	engine.UndoSkippedGone:       "skipped: no longer there (you moved it?)",
	engine.UndoSkippedUnfindable: "skipped: cannot be found (the server renumbered the folder)",
	engine.UndoFailed:            "FAILED",
}

func writeUndoReport(w io.Writer, rep *engine.UndoReport, dryRun bool) error {
	if dryRun {
		_, _ = fmt.Fprintln(w, "dry run: nothing was changed")
	}
	counts := map[string]int{}
	if len(rep.Outcomes) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "MOVED ON\tFROM\tBACK TO\tSTATUS\tWHY IT WAS FILED")
		for _, o := range rep.Outcomes {
			counts[o.Status]++
			when := "-"
			if !o.At.IsZero() {
				when = o.At.Local().Format("2006-01-02 15:04")
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", when, o.From, o.To, undoStatusText[o.Status], o.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w)
	}
	skipped := counts[engine.UndoSkippedGone] + counts[engine.UndoSkippedUnfindable]
	_, _ = fmt.Fprintf(w, "%d restored, %d would restore, %d skipped, %d failed\n",
		counts[engine.UndoRestored], counts[engine.UndoWouldRestore], skipped, counts[engine.UndoFailed])
	return nil
}
