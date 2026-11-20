// Package store persists quietbox state in SQLite (pure Go driver, no CGO).
// It holds sender routes and counts, never message bodies, subjects or keys.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Route sources.
const (
	SourceLearned  = "learned"
	SourceTrained  = "trained" // user moved mail there (M4); never overwritten by learning
	SourceDetected = "detected"
)

// ErrNotInitialised means the database file does not exist yet.
var ErrNotInitialised = errors.New("no quietbox database yet: run `quietbox init` first")

// Store is a handle on the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return open(path)
}

// OpenExisting opens a database that must already exist; it creates nothing.
func OpenExisting(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInitialised
		}
		return nil, err
	}
	return open(path)
}

func open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; avoids SQLITE_BUSY between our own goroutines
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) version() (int, error) {
	var v int
	err := s.db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

func (s *Store) migrate() error {
	v, err := s.version()
	if err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this quietbox understands (%d); upgrade quietbox", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA does not accept bind parameters.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// Row types.

type Obs struct {
	Address string
	Folder  string
	At      time.Time
}

type SentRow struct {
	Address  string
	Count    int
	LastSent time.Time
}

// SentMsg is the Message-ID of one message in the Sent folder.
type SentMsg struct {
	MessageID string
	At        time.Time
}

type FolderRow struct {
	Name        string
	Type        string
	Present     bool
	LastScanned time.Time
}

type SenderRow struct {
	Address     string
	Domain      string
	RouteFolder string
	RouteSource string
	Mixed       bool
	SentTo      int
	Reason      string
	UpdatedAt   time.Time
}

type DomainRow struct {
	Domain   string
	Folder   string
	Senders  int
	Agreeing int
}

type FolderCount struct {
	Address  string
	Folder   string
	Count    int
	LastSeen time.Time
}

// Audit statuses. A mutation is recorded as planned before it runs.
const (
	AuditPlanned = "planned"
	AuditDone    = "done"
	AuditFailed  = "failed"
)

// Audit actions.
const (
	ActionCreateFolder = "create_folder"
	ActionMove         = "move"
	ActionUndoMove     = "undo_move"
)

type AuditRow struct {
	ID              int64
	Action          string
	MessageKey      string
	FromFolder      string
	ToFolder        string
	Reason          string
	At              time.Time
	Undone          bool
	Status          string // AuditPlanned | AuditDone | AuditFailed; "" is stored as done
	DestUIDValidity uint32 // where a moved message landed (from COPYUID)
	DestUID         uint32
}

// Cursor is where the next pass starts in a folder: UIDs below NextUID were
// already handled. It is only valid while the folder's UIDVALIDITY matches.
type Cursor struct {
	Folder      string
	UIDValidity uint32
	NextUID     uint32
}

type SenderFilter struct {
	Folder string // "" = any
	Mixed  bool   // true = only mixed senders
}

// Tx is a write transaction.
type Tx struct{ tx *sql.Tx }

// Update runs fn in one transaction; it commits if fn returns nil.
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Tx{tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func inClause(n int) string {
	return "(" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")"
}

// ReplaceObservations replaces observation rows for the folders in scope, or
// all rows when scope is nil.
func (t *Tx) ReplaceObservations(scope []string, obs []Obs) error {
	if scope == nil {
		if _, err := t.tx.Exec("DELETE FROM observations"); err != nil {
			return err
		}
	} else if len(scope) > 0 {
		args := make([]any, len(scope))
		for i, f := range scope {
			args[i] = f
		}
		if _, err := t.tx.Exec("DELETE FROM observations WHERE folder IN "+inClause(len(scope)), args...); err != nil {
			return err
		}
	}
	stmt, err := t.tx.Prepare("INSERT INTO observations(address, folder, internal_date) VALUES (?,?,?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, o := range obs {
		if _, err := stmt.Exec(o.Address, o.Folder, unix(o.At)); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceSentTo replaces the whole sent_to table.
func (t *Tx) ReplaceSentTo(rows []SentRow) error {
	if _, err := t.tx.Exec("DELETE FROM sent_to"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := t.tx.Exec("INSERT INTO sent_to(address, count, last_sent) VALUES (?,?,?)",
			r.Address, r.Count, unix(r.LastSent)); err != nil {
			return err
		}
	}
	return nil
}

// AddSent merges newly seen sent mail into sent_to (counts add up, the latest
// send date wins) and sent_message_ids (known IDs are kept), without touching
// what is already there.
func (t *Tx) AddSent(rows []SentRow, ids []SentMsg) error {
	for _, r := range rows {
		if _, err := t.tx.Exec(`INSERT INTO sent_to(address, count, last_sent) VALUES (?,?,?)
			ON CONFLICT(address) DO UPDATE SET count = count + excluded.count, last_sent = MAX(last_sent, excluded.last_sent)`,
			r.Address, r.Count, unix(r.LastSent)); err != nil {
			return err
		}
	}
	for _, m := range ids {
		if m.MessageID == "" {
			continue
		}
		if _, err := t.tx.Exec("INSERT OR IGNORE INTO sent_message_ids(message_id, sent_at) VALUES (?,?)",
			m.MessageID, unix(m.At)); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceSentMessageIDs replaces the whole sent_message_ids table. Empty IDs are
// skipped and duplicates collapse.
func (t *Tx) ReplaceSentMessageIDs(rows []SentMsg) error {
	if _, err := t.tx.Exec("DELETE FROM sent_message_ids"); err != nil {
		return err
	}
	stmt, err := t.tx.Prepare("INSERT OR IGNORE INTO sent_message_ids(message_id, sent_at) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if r.MessageID == "" {
			continue
		}
		if _, err := stmt.Exec(r.MessageID, unix(r.At)); err != nil {
			return err
		}
	}
	return nil
}

// UpsertFolder records a configured folder's state.
func (t *Tx) UpsertFolder(f FolderRow) error {
	_, err := t.tx.Exec(`INSERT INTO folders(name, type, present, last_scanned_at) VALUES (?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET type=excluded.type, present=excluded.present, last_scanned_at=excluded.last_scanned_at`,
		f.Name, f.Type, b2i(f.Present), unix(f.LastScanned))
	return err
}

// ReplaceSenders rebuilds learned sender routes. Rows the user trained
// (route_source = trained) are kept and win over any learned row for the same
// address.
func (t *Tx) ReplaceSenders(rows []SenderRow) error {
	if _, err := t.tx.Exec("DELETE FROM senders WHERE route_source <> ?", SourceTrained); err != nil {
		return err
	}
	return t.insertSenders(rows)
}

func (t *Tx) insertSenders(rows []SenderRow) error {
	stmt, err := t.tx.Prepare(`INSERT OR IGNORE INTO senders
		(address, domain, route_folder, route_source, mixed, sent_to_count, reason, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.Exec(r.Address, r.Domain, r.RouteFolder, r.RouteSource,
			b2i(r.Mixed), r.SentTo, r.Reason, unix(r.UpdatedAt)); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceDomains replaces the whole domains table.
func (t *Tx) ReplaceDomains(rows []DomainRow) error {
	if _, err := t.tx.Exec("DELETE FROM domains"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := t.tx.Exec("INSERT INTO domains(domain, default_folder, senders, agreeing) VALUES (?,?,?,?)",
			r.Domain, r.Folder, r.Senders, r.Agreeing); err != nil {
			return err
		}
	}
	return nil
}

// Observations in the transaction (for re-resolving after a scoped replace).
func (t *Tx) Observations() ([]Obs, error) { return queryObs(t.tx) }

// SentTo rows in the transaction.
func (t *Tx) SentTo() ([]SentRow, error) { return querySent(t.tx) }

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryObs(q queryer) ([]Obs, error) {
	rows, err := q.Query("SELECT address, folder, internal_date FROM observations ORDER BY address, folder, internal_date")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Obs
	for rows.Next() {
		var o Obs
		var at int64
		if err := rows.Scan(&o.Address, &o.Folder, &at); err != nil {
			return nil, err
		}
		o.At = fromUnix(at)
		out = append(out, o)
	}
	return out, rows.Err()
}

func querySent(q queryer) ([]SentRow, error) {
	rows, err := q.Query("SELECT address, count, last_sent FROM sent_to ORDER BY address")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SentRow
	for rows.Next() {
		var r SentRow
		var at int64
		if err := rows.Scan(&r.Address, &r.Count, &at); err != nil {
			return nil, err
		}
		r.LastSent = fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Observations returns every observation.
func (s *Store) Observations(context.Context) ([]Obs, error) { return queryObs(s.db) }

// SentTo returns every sent_to row.
func (s *Store) SentTo(context.Context) ([]SentRow, error) { return querySent(s.db) }

// SentMessageIDs returns the Message-IDs of sent mail as a set.
func (s *Store) SentMessageIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT message_id FROM sent_message_ids")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// FolderCounts reads the sender_folder_counts view.
func (s *Store) FolderCounts(ctx context.Context) ([]FolderCount, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT address, folder, count, last_seen FROM sender_folder_counts ORDER BY address, folder")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FolderCount
	for rows.Next() {
		var c FolderCount
		var at int64
		if err := rows.Scan(&c.Address, &c.Folder, &c.Count, &at); err != nil {
			return nil, err
		}
		c.LastSeen = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Senders lists sender routes, ordered by address.
func (s *Store) Senders(ctx context.Context, f SenderFilter) ([]SenderRow, error) {
	q := "SELECT address, domain, route_folder, route_source, mixed, sent_to_count, reason, updated_at FROM senders WHERE 1=1"
	var args []any
	if f.Folder != "" {
		q += " AND route_folder = ?"
		args = append(args, f.Folder)
	}
	if f.Mixed {
		q += " AND mixed = 1"
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY address", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SenderRow
	for rows.Next() {
		var r SenderRow
		var mixed int
		var at int64
		if err := rows.Scan(&r.Address, &r.Domain, &r.RouteFolder, &r.RouteSource, &mixed, &r.SentTo, &r.Reason, &at); err != nil {
			return nil, err
		}
		r.Mixed, r.UpdatedAt = mixed == 1, fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Domains lists domain defaults, ordered by domain.
func (s *Store) Domains(ctx context.Context) ([]DomainRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT domain, default_folder, senders, agreeing FROM domains ORDER BY domain")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DomainRow
	for rows.Next() {
		var r DomainRow
		if err := rows.Scan(&r.Domain, &r.Folder, &r.Senders, &r.Agreeing); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Folders lists the recorded configured folders, ordered by name.
func (s *Store) Folders(ctx context.Context) ([]FolderRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name, type, present, last_scanned_at FROM folders ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FolderRow
	for rows.Next() {
		var r FolderRow
		var present int
		var at int64
		if err := rows.Scan(&r.Name, &r.Type, &present, &at); err != nil {
			return nil, err
		}
		r.Present, r.LastScanned = present == 1, fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddAudit appends an audit row and commits it immediately, on its own, so it
// is durable before the mailbox action it describes is attempted.
func (s *Store) AddAudit(ctx context.Context, a AuditRow) (int64, error) {
	if a.Status == "" {
		a.Status = AuditDone
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO audit(action, message_key, from_folder, to_folder, reason, at, status, dest_uidvalidity, dest_uid) VALUES (?,?,?,?,?,?,?,?,?)",
		a.Action, a.MessageKey, a.FromFolder, a.ToFolder, a.Reason, unix(a.At), a.Status, a.DestUIDValidity, a.DestUID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const auditCols = "id, action, message_key, from_folder, to_folder, reason, at, undone, status, dest_uidvalidity, dest_uid"

func scanAudit(rows *sql.Rows) ([]AuditRow, error) {
	defer func() { _ = rows.Close() }()
	var out []AuditRow
	for rows.Next() {
		var a AuditRow
		var at int64
		var undone int
		if err := rows.Scan(&a.ID, &a.Action, &a.MessageKey, &a.FromFolder, &a.ToFolder, &a.Reason, &at, &undone,
			&a.Status, &a.DestUIDValidity, &a.DestUID); err != nil {
			return nil, err
		}
		a.At, a.Undone = fromUnix(at), undone == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// Audit returns the audit log, oldest first.
func (s *Store) Audit(ctx context.Context) ([]AuditRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+auditCols+" FROM audit ORDER BY id")
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// AuditSince returns rows of one action recorded at or after since, newest first.
func (s *Store) AuditSince(ctx context.Context, since time.Time, action string) ([]AuditRow, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+auditCols+" FROM audit WHERE action = ? AND at >= ? ORDER BY id DESC", action, unix(since))
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// SetAuditResult records how a planned action ended and where a moved message landed.
func (s *Store) SetAuditResult(ctx context.Context, id int64, status string, destUIDValidity, destUID uint32) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE audit SET status = ?, dest_uidvalidity = ?, dest_uid = ? WHERE id = ?",
		status, destUIDValidity, destUID, id)
	return err
}

// PlannedMoves returns move rows whose outcome was never recorded (a run was
// interrupted between the audit row and the end of the move), oldest first.
func (s *Store) PlannedMoves(ctx context.Context) ([]AuditRow, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+auditCols+" FROM audit WHERE action = ? AND status = ? ORDER BY id", ActionMove, AuditPlanned)
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// MarkUndone flags an audit row as reversed by `quietbox undo`.
func (s *Store) MarkUndone(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE audit SET undone = 1 WHERE id = ?", id)
	return err
}

// MovedKeys returns the message key of every move ever recorded, whatever its
// status and whether or not it was undone. Such a message is never moved again.
func (s *Store) MovedKeys(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT message_key FROM audit WHERE action = ? AND message_key <> ''", ActionMove)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = struct{}{}
	}
	return out, rows.Err()
}

// SetCursor stores or replaces a folder's cursor inside the transaction.
func (t *Tx) SetCursor(c Cursor) error {
	_, err := t.tx.Exec(setCursorSQL, c.Folder, c.UIDValidity, c.NextUID)
	return err
}

const setCursorSQL = "INSERT INTO cursors(folder, uidvalidity, next_uid) VALUES (?,?,?) ON CONFLICT(folder) DO UPDATE SET uidvalidity=excluded.uidvalidity, next_uid=excluded.next_uid"

// Cursor returns the stored cursor for a folder; ok is false when there is none.
func (s *Store) Cursor(ctx context.Context, folder string) (Cursor, bool, error) {
	c := Cursor{Folder: folder}
	err := s.db.QueryRowContext(ctx, "SELECT uidvalidity, next_uid FROM cursors WHERE folder = ?", folder).
		Scan(&c.UIDValidity, &c.NextUID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// SetCursor stores or replaces a folder's cursor.
func (s *Store) SetCursor(ctx context.Context, c Cursor) error {
	_, err := s.db.ExecContext(ctx, setCursorSQL, c.Folder, c.UIDValidity, c.NextUID)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
