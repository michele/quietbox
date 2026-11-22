package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MetaSweptAt is the meta key holding when the blackhole sweep last ran
// (Unix seconds, as text).
const MetaSweptAt = "blackhole_swept_at"

// SeenRow is the first time a message was seen in the blackhole folder.
type SeenRow struct {
	Key       string
	FirstSeen time.Time
}

// BlackholeSeen returns the first-seen time of every message the sweep has
// seen in the blackhole folder, by key.
func (s *Store) BlackholeSeen(ctx context.Context) (map[string]SeenRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, first_seen FROM blackhole_seen")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]SeenRow{}
	for rows.Next() {
		var r SeenRow
		var at int64
		if err := rows.Scan(&r.Key, &at); err != nil {
			return nil, err
		}
		r.FirstSeen = fromUnix(at)
		out[r.Key] = r
	}
	return out, rows.Err()
}

// ReplaceBlackholeSeen makes the stored first-seen rows exactly rows: keys not
// in it (messages that left the folder) are dropped.
func (t *Tx) ReplaceBlackholeSeen(rows map[string]SeenRow) error {
	if _, err := t.tx.Exec("DELETE FROM blackhole_seen"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := t.tx.Exec("INSERT INTO blackhole_seen(key, first_seen) VALUES (?, ?)", r.Key, unix(r.FirstSeen)); err != nil {
			return err
		}
	}
	return nil
}

// Meta returns a value from the meta table; ok is false when it is not set.
func (s *Store) Meta(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetMeta stores or replaces a value in the meta table.
func (t *Tx) SetMeta(key, value string) error {
	_, err := t.tx.Exec("INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}
