package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
)

func newExplainCmd(opts *options) *cobra.Command {
	var folder string
	cmd := &cobra.Command{
		Use:   "explain [--folder F] <uid | message-id>",
		Short: "Show what the classifier would decide for one message now, and why",
		Long: `Looks up one message (a number is a UID, anything else a Message-ID) and shows
the rule and reason the classifier would use for it today, the sender's route and
the training event behind it, and the audit rows for the message.

It only reads: the folder is opened read-only, nothing is written, and it does
not take the instance lock. The output contains the sender address but never
the subject or the body.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eo := engine.ExplainOptions{Folder: folder}
			if n, err := strconv.ParseUint(args[0], 10, 32); err == nil && n > 0 {
				eo.UID = uint32(n)
			} else {
				eo.MessageID = "<" + strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(args[0]), "<"), ">") + ">"
			}
			cfg, st, b, err := openMailbox(cmd, opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			defer func() { _ = b.Close() }()
			if eo.Folder == "" {
				eo.Folder = inboxName(cfg)
			}
			e, err := engine.Explain(cmd.Context(), cfg, b, st, eo)
			if err != nil {
				return err
			}
			writeExplanation(cmd.OutOrStdout(), eo.Folder, e)
			return nil
		},
	}
	cmd.Flags().StringVar(&folder, "folder", "", "folder the message is in (default: INBOX)")
	return cmd
}

// inboxName is the configured INBOX folder name.
func inboxName(cfg *config.Config) string {
	for _, f := range cfg.Folders {
		if f.Type == config.TypeImportant {
			return f.Name
		}
	}
	return "INBOX"
}

// writeExplanation prints sender, folder, UID, Message-ID and the verdict;
// never the subject or any body.
func writeExplanation(w io.Writer, folder string, e *engine.Explanation) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("This is what the classifier would decide now, with what quietbox knows today.")
	p("")
	p("sender:     %s", orNone(e.Header.From))
	p("folder:     %s (UID %d)", folder, e.Header.UID)
	p("message-id: %s", orNone(e.Header.MessageID))
	p("")
	p("decision:   %s", e.Decision.Folder)
	p("rule:       %s", e.Decision.Rule)
	p("reason:     %s", e.Decision.Reason)
	if len(e.Decision.Signals) > 0 {
		p("signals:    %s", strings.Join(e.Decision.Signals, ", "))
	}
	p("")
	if e.Route == nil {
		p("route:      none for this sender")
	} else {
		mixed := ""
		if e.Route.Mixed {
			mixed = ", mixed"
		}
		p("route:      %s (%s%s)", e.Route.RouteFolder, e.Route.RouteSource, mixed)
	}
	if len(e.Training) > 0 {
		t := e.Training[0]
		p("training:   latest: %s -> %s (%s), %s", t.Sender, t.Folder, t.Source, t.At.Format("2006-01-02 15:04"))
	}
	if len(e.Audit) > 0 {
		p("")
		p("audit:")
		for _, a := range e.Audit {
			status := a.Status
			if a.Undone {
				status += ", undone"
			}
			p("  %s %s -> %s, %s, %s", a.Action, a.FromFolder, a.ToFolder, status, a.At.Format("2006-01-02 15:04"))
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
