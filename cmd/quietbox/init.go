package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
	"github.com/michele/quietbox/internal/store"
)

type initFlags struct {
	since    string
	folder   string
	dryRun   bool
	yes      bool
	report   string
	now      string
	fresh    bool
	backlog  string
	lockWait string
}

func newInitCmd(opts *options) *cobra.Command {
	f := &initFlags{}
	cmd := &cobra.Command{
		Use:     "init",
		Aliases: []string{"learn"},
		Short:   "Learn where your mail goes today and create missing folders (safe to re-run)",
		Long: `Scans the headers of the configured folders and Sent (read-only: nothing is
marked as read and no body is downloaded), works out a route for every sender,
and stores the result. The only change it can make to your mailbox is creating
configured folders that do not exist yet, and only with your confirmation.

It always writes a CSV report so you can review the result before any mail is
moved: by default next to the database, named after it (quietbox.db ->
quietbox.learn-report.csv), also on a dry run. Running it again rebuilds the
learned data and replaces the report.

For an account that was never managed by SaneBox or similar, use --fresh: INBOX
history says nothing about where mail belongs, so it is not learned as routes.
Add --backlog 30d to let the first 'quietbox once' also consider the INBOX mail
of the last 30 days (read mail included, flagged mail excluded). init itself
never moves anything: review with 'quietbox once --dry-run', then run 'once'.

While another quietbox works on the same mailbox (a 'run' pass, 'once', 'undo')
it waits for it, at most --lock-timeout, then fails. A dry run never waits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runInit(cmd, opts, f) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.since, "since", "", "limit scan depth, e.g. 2y or 90d (INBOX is also capped by learn.inbox_lookback)")
	fl.StringVar(&f.folder, "folder", "", "re-learn only this configured folder (needs a previous full run)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "change nothing (no folders, no database); still prints the summary and writes the report")
	fl.BoolVarP(&f.yes, "yes", "y", false, "create missing folders without asking")
	fl.BoolVar(&f.fresh, "fresh", false, "account never managed before: do not learn routes from INBOX history")
	fl.StringVar(&f.backlog, "backlog", "", "with --fresh: let 'once' also sort INBOX mail from this long ago, e.g. 30d")
	fl.StringVar(&f.report, "report", "", "where to write the CSV report (default: next to the database, e.g. quietbox.db -> quietbox.learn-report.csv; empty to skip)")
	fl.StringVar(&f.now, "now", "", "treat this date (YYYY-MM-DD) as today; for tests and demos")
	_ = fl.MarkHidden("now")
	addLockTimeoutFlag(cmd, &f.lockWait)
	return cmd
}

func runInit(cmd *cobra.Command, opts *options, f *initFlags) error {
	var backlog time.Duration
	switch {
	case f.backlog != "" && !f.fresh:
		return fmt.Errorf("--backlog requires --fresh")
	case f.fresh && f.folder != "":
		return fmt.Errorf("--fresh learns a whole account: it cannot be combined with --folder")
	case f.backlog != "":
		d, err := config.ParseDuration(f.backlog)
		if err != nil {
			return fmt.Errorf("--backlog: %w", err)
		}
		backlog = d
	}
	timeout, err := parseLockTimeout(f.lockWait)
	if err != nil {
		return err
	}
	cfg, err := opts.loadConfig(false)
	if err != nil {
		return err
	}
	lo := engine.LearnOptions{
		Folder:   f.folder,
		DryRun:   f.dryRun,
		Yes:      f.yes,
		Fresh:    f.fresh,
		Backlog:  backlog,
		Confirm:  confirmer(cmd),
		Progress: func(msg string) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg) },

		LockTimeout: timeout,
		OnLockWait:  lockWaitNotice(cmd),
	}
	if lo.Now, err = parseNow(f.now); err != nil {
		return err
	}
	if f.since != "" {
		if lo.Since, err = config.ParseDuration(f.since); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
	}

	b, err := imapbackend.Dial(cmd.Context(), imapbackend.Options{
		Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS,
		Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
	})
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()

	// Open the database only after connecting, so a failed login leaves no
	// stray file behind. A dry run never creates one.
	var st *store.Store
	switch {
	case f.folder != "":
		st, err = openState(cmd.Context(), cfg, false)
	case !f.dryRun:
		st, err = openState(cmd.Context(), cfg, true)
	}
	if err != nil {
		return err
	}
	if st != nil {
		defer func() { _ = st.Close() }()
	}

	rep, err := engine.Learn(cmd.Context(), cfg, b, st, lo)
	if err != nil {
		return err
	}
	if err := rep.Write(cmd.OutOrStdout()); err != nil {
		return err
	}
	if path, isDefault := reportTarget(cmd, f.report, cfg.State.Path); path != "" {
		emitReport(cmd, rep, path, isDefault)
	}
	return nil
}

// defaultReportPath is the report next to the database, named after it:
// <dir>/quietbox.db -> <dir>/quietbox.learn-report.csv. The state path is already
// account-aware, so two accounts never share a report.
func defaultReportPath(statePath string) string {
	dir, file := filepath.Split(statePath)
	name := strings.TrimSuffix(file, ".db")
	if name == "" { // "/srv/state/" or "/srv/state/.db": no hidden ".learn-report.csv"
		name = "quietbox"
	}
	return filepath.Join(dir, name+".learn-report.csv")
}

// reportTarget resolves --report: not given means the default next to the
// database (isDefault), given empty means no report (path "").
func reportTarget(cmd *cobra.Command, report, statePath string) (path string, isDefault bool) {
	if cmd.Flags().Changed("report") {
		return report, false
	}
	return defaultReportPath(statePath), true
}

// emitReport writes the CSV report. By now init has saved its state and printed
// its summary, so a failure is a warning, not an error exit.
func emitReport(cmd *cobra.Command, rep *engine.LearnReport, path string, isDefault bool) {
	if err := writeReportFile(path, isDefault, rep.WriteCSV); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write the report to %s: %v\n", path, err)
		return
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nReport written to %s\n", path)
}

// writeReportFile writes a private (0600) file atomically: a temporary file in
// the same directory, renamed over the target, so a crash or a full disk never
// leaves a truncated report. mkdir creates the parent directory (0700) first. A
// target that exists and is not a regular file (/dev/stdout, a named pipe) is
// written in place, since a rename would replace it; a symlink is followed.
func writeReportFile(path string, mkdir bool, write func(io.Writer) error) (err error) {
	defer func() {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // the caller already names the report path
		}
	}()
	if mkdir {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			var pe *fs.PathError
			if errors.As(err, &pe) {
				return fmt.Errorf("create %s: %w", pe.Path, pe.Err)
			}
			return err
		}
	}
	// Device paths first, before resolving symlinks: /dev/stdout resolves to the
	// file stdout is redirected to, and renaming over it would lose init's output.
	if isDevicePath(path) {
		return writeDevice(path, write)
	}
	// A dangling symlink fails to resolve and is replaced by the report itself:
	// the user chose that path.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		return writeInPlace(path, write)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*") // mode 0600
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = write(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func isDevicePath(path string) bool {
	clean := filepath.Clean(path)
	return strings.HasPrefix(clean, "/dev/") || strings.HasPrefix(clean, "/proc/")
}

// writeDevice appends to an existing device path such as /dev/stdout or
// /dev/null: no create, no truncate, so a redirect target keeps what was
// already written to it.
func writeDevice(path string, write func(io.Writer) error) (err error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	return write(out)
}

func writeInPlace(path string, write func(io.Writer) error) (err error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	return write(out)
}

func parseNow(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--now: want YYYY-MM-DD, got %q", s)
	}
	return t, nil
}

// confirmer returns a prompt function when input is interactive (a terminal, or
// an injected reader in tests), and nil otherwise, so cron runs never hang.
func confirmer(cmd *cobra.Command) func(string) (bool, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok {
		if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return nil
		}
	}
	r := bufio.NewReader(in)
	return func(name string) (bool, error) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Create folder %q? [y/N] ", name)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF {
				return false, nil
			}
			return false, err
		}
		return answerYes(line), nil
	}
}

func answerYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
