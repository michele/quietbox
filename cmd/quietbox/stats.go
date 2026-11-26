package main

import (
	"encoding/json"
	"time"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/stats"
	"github.com/michele/quietbox/internal/store"
)

func newStatsCmd(opts *options) *cobra.Command {
	var since string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show how much mail quietbox filed, where it went and how often it was wrong (reads the local database only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			window, err := config.ParseDuration(since)
			if err != nil {
				return err
			}
			cfg, err := opts.loadConfig(true)
			if err != nil {
				return err
			}
			st, err := openState(cmd.Context(), cfg, false)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			now := time.Now()
			start := now.Add(-window)
			audit, err := st.AuditSince(cmd.Context(), start, store.ActionMove)
			if err != nil {
				return err
			}
			training, err := st.TrainingSince(cmd.Context(), start)
			if err != nil {
				return err
			}
			in := stats.Input{
				Now: now, Window: window, SecondsPerMessage: cfg.Stats.SecondsPerMessage,
				Inbox: "INBOX", RetentionRule: engine.RuleRetention,
			}
			for _, f := range cfg.Folders {
				in.FolderOrder = append(in.FolderOrder, f.Name)
			}
			for _, a := range audit {
				in.Moves = append(in.Moves, stats.Move{
					Key: a.MessageKey, From: a.FromFolder, To: a.ToFolder, Reason: a.Reason,
					At: a.At, Done: a.Status == store.AuditDone, Undone: a.Undone,
				})
			}
			for _, e := range training {
				in.Events = append(in.Events, stats.Event{
					At: e.At, Sender: e.Sender, Folder: e.Folder, Key: e.MessageKey, ByMove: e.Source == store.TrainMove,
				})
			}
			report := stats.Compute(in)

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			account := cfg.Account
			if account == "" {
				account = cfg.IMAP.Username
			}
			return stats.WriteText(cmd.OutOrStdout(), report, account)
		},
	}
	cmd.Flags().StringVar(&since, "since", "30d", "how far back to look (e.g. 7d, 30d, 6mo)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}
