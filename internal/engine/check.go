// Package engine orchestrates mailbox operations. M0 only has the read-only
// Check; all later mutations must go through this package.
package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
)

// FolderStatus is one configured folder as found on the server.
type FolderStatus struct {
	Name         string
	Type         config.FolderType
	Exists       bool
	Messages     uint32
	DetectTarget bool
}

// Report is the result of Check.
type Report struct {
	Capabilities mail.Capabilities
	ObjectID     bool
	Folders      []FolderStatus
	Warnings     []string
}

// Check compares the config with the server. It is read-only.
func Check(ctx context.Context, cfg *config.Config, b mail.Backend) (*Report, error) {
	caps, err := b.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	server, err := b.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]mail.Folder{}
	for _, f := range server {
		have[f.Name] = f
	}
	targets := cfg.DetectionTargets()
	r := &Report{Capabilities: caps, ObjectID: caps.HasObjectID(), Warnings: cfg.Warnings()}
	for _, cf := range cfg.Folders {
		st := FolderStatus{Name: cf.Name, Type: cf.Type, DetectTarget: targets[cf.Type] == cf.Name}
		sf, ok := have[cf.Name]
		if !ok && strings.EqualFold(cf.Name, "INBOX") {
			sf, ok = have["INBOX"]
		}
		if ok {
			st.Exists, st.Messages = true, sf.Messages
		}
		r.Folders = append(r.Folders, st)
	}
	return r, nil
}

// Write renders the report as plain text.
func (r *Report) Write(w io.Writer) {
	fmt.Fprintf(w, "Server capabilities: %s\n", strings.Join(r.Capabilities, " "))
	if r.ObjectID {
		fmt.Fprintln(w, "Message key: EMAILID (server supports OBJECTID)")
	} else {
		fmt.Fprintln(w, "Message key: fallback to Message-ID header (server lacks OBJECTID)")
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FOLDER\tTYPE\tSTATUS\tMESSAGES\tDETECTION")
	for _, f := range r.Folders {
		status, msgs := "missing", "-"
		if f.Exists {
			status, msgs = "present", fmt.Sprint(f.Messages)
		}
		detect := "-"
		switch {
		case f.Type == config.TypeBlackhole:
			detect = "never (manual only)"
		case f.DetectTarget:
			detect = "target"
		}
		if !f.Exists {
			detect += " | init will create it"
			detect = strings.TrimPrefix(detect, "- | ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Name, f.Type, status, msgs, detect)
	}
	tw.Flush()
	if len(r.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warn := range r.Warnings {
			fmt.Fprintf(w, "  - %s\n", warn)
		}
	}
}
