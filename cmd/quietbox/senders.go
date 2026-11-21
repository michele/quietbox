package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/store"
)

func newSendersCmd(opts *options) *cobra.Command {
	var filter store.SenderFilter
	cmd := &cobra.Command{
		Use:   "senders",
		Short: "List learned sender routes (reads the local database only, no IMAP connection)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.loadConfig(true)
			if err != nil {
				return err
			}
			st, err := openState(cmd.Context(), cfg, false)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			rows, err := st.Senders(cmd.Context(), filter)
			if err != nil {
				return err
			}
			counts, err := st.FolderCounts(cmd.Context())
			if err != nil {
				return err
			}
			byAddr := map[string]map[string]int{}
			for _, c := range counts {
				if byAddr[c.Address] == nil {
					byAddr[c.Address] = map[string]int{}
				}
				byAddr[c.Address][c.Folder] = c.Count
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "ADDRESS\tROUTE\tSOURCE\tMIXED\tSENT_TO\tHISTORY")
			for _, r := range rows {
				var hist []string
				for _, f := range cfg.Folders { // config order keeps columns stable
					if n := byAddr[r.Address][f.Name]; n > 0 {
						hist = append(hist, fmt.Sprintf("%s:%d", f.Name, n))
					}
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%d\t%s\n",
					r.Address, r.RouteFolder, r.RouteSource, r.Mixed, r.SentTo, strings.Join(hist, " "))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%d senders\n", len(rows))
			return err
		},
	}
	cmd.Flags().StringVar(&filter.Folder, "folder", "", "only senders routed to this folder")
	cmd.Flags().BoolVar(&filter.Mixed, "mixed", false, "only mixed senders")
	cmd.AddCommand(newSendersSetCmd(opts), newSendersUnsetCmd(opts))
	return cmd
}

// lockedState waits for the instance lock (a running `quietbox run` holds it
// until it stops) and opens the existing state. The returned func releases both.
func lockedState(cmd *cobra.Command, cfg *config.Config) (*store.Store, func(), error) {
	release, err := engine.AcquireLockWait(cmd.Context(), engine.LockPath(cfg), lockWaitNotice(cmd))
	if err != nil {
		return nil, nil, err
	}
	st, err := openState(cmd.Context(), cfg, false)
	if err != nil {
		release()
		return nil, nil, err
	}
	return st, func() { _ = st.Close(); release() }, nil
}

func newSendersSetCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "set <address> <folder>",
		Short: "Always file a sender's mail into a folder (waits while another quietbox runs)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := opts.loadConfig(true)
			if err != nil {
				return err
			}
			// Validated before waiting, so a typo does not wait for `run` to stop.
			addr, err := engine.CheckSenderRoute(cfg, args[0], args[1])
			if err != nil {
				return err
			}
			st, done, err := lockedState(cmd, cfg)
			if err != nil {
				return err
			}
			defer done()
			if err := engine.SetSenderRoute(cmd.Context(), cfg, st, addr, args[1], time.Now()); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s (trained)\n", addr, args[1])
			return err
		},
	}
}

func newSendersUnsetCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <address>",
		Short: "Remove a trained route; learned routes are left alone (waits while another quietbox runs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := opts.loadConfig(true)
			if err != nil {
				return err
			}
			st, done, err := lockedState(cmd, cfg)
			if err != nil {
				return err
			}
			defer done()
			addr := strings.ToLower(strings.TrimSpace(args[0]))
			removed, err := engine.UnsetSenderRoute(cmd.Context(), st, addr)
			if err != nil {
				return err
			}
			if removed {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: trained route removed\n", addr)
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "no trained route for %s\n", addr)
			}
			return err
		},
	}
}
