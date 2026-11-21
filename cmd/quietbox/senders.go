package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

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
	return cmd
}
