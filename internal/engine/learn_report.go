package engine

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
)

// Write renders a human summary of the report.
func (r *LearnReport) Write(w io.Writer) error {
	var buf bytes.Buffer
	mode := ""
	if r.DryRun {
		mode = " (dry run: nothing was changed)"
	}
	scope := "all folders"
	if r.Scope != "" {
		scope = "folder " + r.Scope
	}
	fmt.Fprintf(&buf, "Learned routes for %d senders from %d messages in %s%s\n", len(r.Senders), r.Scanned, scope, mode)
	if r.Scope == "" {
		fmt.Fprintf(&buf, "Sent mail: %d messages to %d recipients\n", r.SentMessages, r.SentRecipients)
	}
	fmt.Fprintf(&buf, "Skipped: %d messages without a usable From, %d from your own addresses\n\n", r.SkippedNoFrom, r.SkippedSelf)

	perFolder, mixed := map[string]int{}, map[string]int{}
	for _, s := range r.Senders {
		perFolder[s.Folder]++
		if s.Mixed {
			mixed[s.Folder]++
		}
	}
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "FOLDER\tTYPE\tSTATUS\tSCANNED\tSENDERS\tMIXED")
	for _, f := range r.Folders {
		status := "missing"
		if f.Present {
			status = "present"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n", f.Name, f.Type, status, f.Scanned, perFolder[f.Name], mixed[f.Name])
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(r.Created) > 0 {
		fmt.Fprintf(&buf, "\nCreated folders: %s\n", join(r.Created))
	}
	if len(r.WouldCreate) > 0 {
		fmt.Fprintf(&buf, "\nWould create folders: %s\n", join(r.WouldCreate))
	}
	if len(r.SkippedCreate) > 0 {
		fmt.Fprintf(&buf, "\nNot created (no confirmation; use --yes): %s\n", join(r.SkippedCreate))
	}
	if len(r.Domains) > 0 {
		fmt.Fprintln(&buf, "\nDomain defaults:")
		for _, d := range r.Domains {
			fmt.Fprintf(&buf, "  %s -> %s (%d of %d senders)\n", d.Domain, d.Folder, d.Agreeing, d.Senders)
		}
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func join(s []string) string {
	var b bytes.Buffer
	for i, x := range s {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(x)
	}
	return b.String()
}

// WriteCSV writes learn-report.csv: one row per sender, then one per domain
// default, with a raw message-count column per configured folder.
func (r *LearnReport) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	head := []string{"kind", "address_or_domain", "route_folder", "route_source", "mixed", "sent_to"}
	for _, f := range r.Folders {
		head = append(head, f.Name)
	}
	head = append(head, "last_seen", "reason")
	if err := cw.Write(head); err != nil {
		return err
	}
	for _, s := range r.Senders {
		st := r.Stats[s.Address]
		row := []string{"sender", s.Address, s.Folder, "learned", strconv.FormatBool(s.Mixed), strconv.Itoa(s.SentTo)}
		for _, f := range r.Folders {
			row = append(row, strconv.Itoa(st.Counts[f.Name]))
		}
		last := ""
		if !st.LastSeen.IsZero() {
			last = st.LastSeen.UTC().Format("2006-01-02")
		}
		if err := cw.Write(append(row, last, s.Reason)); err != nil {
			return err
		}
	}
	for _, d := range r.Domains {
		row := []string{"domain", d.Domain, d.Folder, "domain", "false", ""}
		for range r.Folders {
			row = append(row, "")
		}
		reason := fmt.Sprintf("%d of %d senders route here", d.Agreeing, d.Senders)
		if err := cw.Write(append(row, "", reason)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
