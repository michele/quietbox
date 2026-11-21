package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Training sources.
const (
	TrainMove   = "move"   // quietbox saw the user move a message
	TrainManual = "manual" // the user trained a sender explicitly
)

// MessageRow is where a message was last seen.
type MessageRow struct {
	Key          string
	Folder       string
	InternalDate time.Time
}

// TrainingRow is one filing action by the user.
type TrainingRow struct {
	ID         int64
	At         time.Time
	Sender     string
	Folder     string
	Source     string // TrainMove | TrainManual
	MessageKey string
}

// PutMessages upserts message locations by key.
func (t *Tx) PutMessages(rows []MessageRow) error {
	stmt, err := t.tx.Prepare(`INSERT INTO messages(key, folder, internal_date) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET folder=excluded.folder, internal_date=excluded.internal_date`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.Exec(r.Key, r.Folder, unix(r.InternalDate)); err != nil {
			return err
		}
	}
	return nil
}

// ForgetMessages removes messages by key.
func (t *Tx) ForgetMessages(keys []string) error {
	for _, k := range keys {
		if _, err := t.tx.Exec("DELETE FROM messages WHERE key = ?", k); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceMessages replaces the rows of the folders in scope, or all rows when
// scope is nil, with rows.
func (t *Tx) ReplaceMessages(scope []string, rows []MessageRow) error {
	if scope == nil {
		if _, err := t.tx.Exec("DELETE FROM messages"); err != nil {
			return err
		}
	} else if len(scope) > 0 {
		args := make([]any, len(scope))
		for i, f := range scope {
			args[i] = f
		}
		if _, err := t.tx.Exec("DELETE FROM messages WHERE folder IN "+inClause(len(scope)), args...); err != nil {
			return err
		}
	}
	return t.PutMessages(rows)
}

// Messages returns every remembered message, by key.
func (s *Store) Messages(ctx context.Context) (map[string]MessageRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, folder, internal_date FROM messages")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]MessageRow{}
	for rows.Next() {
		var r MessageRow
		var at int64
		if err := rows.Scan(&r.Key, &r.Folder, &at); err != nil {
			return nil, err
		}
		r.InternalDate = fromUnix(at)
		out[r.Key] = r
	}
	return out, rows.Err()
}

// ForgetMessage removes one message by key.
func (s *Store) ForgetMessage(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE key = ?", key)
	return err
}

// PutMessage upserts one message location.
func (s *Store) PutMessage(ctx context.Context, r MessageRow) error {
	return s.Update(ctx, func(t *Tx) error { return t.PutMessages([]MessageRow{r}) })
}

// UpsertTrainedSender stores a trained route for the sender, replacing any
// learned row. It forces route_source=trained and mixed=0, and keeps the
// sent_to_count of an existing row.
func (t *Tx) UpsertTrainedSender(r SenderRow) error {
	_, err := t.tx.Exec(`INSERT INTO senders
		(address, domain, route_folder, route_source, mixed, sent_to_count, reason, updated_at)
		VALUES (?,?,?,?,0,?,?,?)
		ON CONFLICT(address) DO UPDATE SET domain=excluded.domain, route_folder=excluded.route_folder,
			route_source=excluded.route_source, mixed=0, reason=excluded.reason, updated_at=excluded.updated_at`,
		r.Address, r.Domain, r.RouteFolder, SourceTrained, r.SentTo, r.Reason, unix(r.UpdatedAt))
	return err
}

// DeleteTrainedSender removes a trained route; learned rows are never touched.
// It reports whether a row was deleted.
func (s *Store) DeleteTrainedSender(ctx context.Context, address string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM senders WHERE address = ? AND route_source = ?", address, SourceTrained)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AddTraining appends a training event.
func (t *Tx) AddTraining(r TrainingRow) error {
	_, err := t.tx.Exec("INSERT INTO training(at, sender, folder, source, message_key) VALUES (?,?,?,?,?)",
		unix(r.At), r.Sender, r.Folder, r.Source, r.MessageKey)
	return err
}

// TrainingFor returns a sender's training events, newest first.
func (s *Store) TrainingFor(ctx context.Context, sender string) ([]TrainingRow, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, at, sender, folder, source, message_key FROM training WHERE sender = ? ORDER BY at DESC, id DESC", sender)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TrainingRow
	for rows.Next() {
		var r TrainingRow
		var at int64
		if err := rows.Scan(&r.ID, &at, &r.Sender, &r.Folder, &r.Source, &r.MessageKey); err != nil {
			return nil, err
		}
		r.At = fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Sender returns one sender route; ok is false when there is none.
func (s *Store) Sender(ctx context.Context, address string) (SenderRow, bool, error) {
	var r SenderRow
	var mixed int
	var at int64
	err := s.db.QueryRowContext(ctx,
		"SELECT address, domain, route_folder, route_source, mixed, sent_to_count, reason, updated_at FROM senders WHERE address = ?", address).
		Scan(&r.Address, &r.Domain, &r.RouteFolder, &r.RouteSource, &mixed, &r.SentTo, &r.Reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return SenderRow{}, false, nil
	}
	if err != nil {
		return SenderRow{}, false, err
	}
	r.Mixed, r.UpdatedAt = mixed == 1, fromUnix(at)
	return r, true, nil
}
