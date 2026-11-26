package stats

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// FormatSaved renders a number of seconds as "42 s", "34 min" or "2 h 5 min".
func FormatSaved(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d min", seconds/60)
	}
	h, m := seconds/3600, seconds%3600/60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// WriteText prints the report for people. It never prints addresses.
func WriteText(w io.Writer, r Report, account string) error {
	days := int((r.Until.Sub(r.Since) + 12*time.Hour) / (24 * time.Hour))
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	var err error
	p := func(format string, a ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format, a...)
		}
	}
	p("Last %d %s (account: %s)\n", days, unit, account)
	saved := fmt.Sprintf("est. time saved ~%s", FormatSaved(r.SavedSeconds))
	if r.Filed > 0 {
		saved += fmt.Sprintf(" (%d s each)", r.SecondsPerMessage)
	}
	p("Filed away  %d messages   %s\n", r.Filed, saved)

	if len(r.Folders) > 0 {
		p("\nWhere it went\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, f := range r.Folders {
			var parts []string
			for _, rc := range f.Rules {
				parts = append(parts, fmt.Sprintf("%s %d", rc.Rule, rc.Count))
			}
			if f.Purged > 0 {
				parts = append(parts, fmt.Sprintf("purged to Trash %d", f.Purged))
			}
			_, e := fmt.Fprintf(tw, "  %s\t%d\t%.0f%%\t%s\n", f.Folder, f.Count, f.Share*100, strings.Join(parts, " · "))
			if err == nil {
				err = e
			}
		}
		if e := tw.Flush(); err == nil {
			err = e
		}
	}

	p("\nMistakes\n")
	if r.Filed == 0 {
		p("  Corrected by you   %d  (error rate n/a)\n", r.Corrected)
	} else {
		p("  Corrected by you   %d  %.1f%%   (dragged back to INBOX %d, re-filed elsewhere %d)\n",
			r.Corrected, r.ErrorRate*100, r.BackToInbox, r.Refiled)
	}
	p("  Undone with undo   %d\n", r.Undone)
	p("  Senders retrained  %d\n", r.Retrained)
	if r.Filed > 0 {
		p("  The error rate is a floor: it counts only mistakes you fixed, and only the first move per retrained sender; mail wrongly hidden that you never noticed is not in it.\n")
	}
	return err
}
