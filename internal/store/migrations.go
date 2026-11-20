package store

// migrations are applied in order; the schema version is PRAGMA user_version.
// Never edit an applied migration, append a new one.
var migrations = []string{
	// 1: learning state and the audit log.
	`
CREATE TABLE folders (
	name            TEXT PRIMARY KEY,
	type            TEXT NOT NULL,
	present         INTEGER NOT NULL,
	last_scanned_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE observations (
	address       TEXT NOT NULL,
	folder        TEXT NOT NULL,
	internal_date INTEGER NOT NULL
);
CREATE INDEX observations_folder  ON observations(folder);
CREATE INDEX observations_address ON observations(address);
CREATE TABLE sent_to (
	address   TEXT PRIMARY KEY,
	count     INTEGER NOT NULL,
	last_sent INTEGER NOT NULL
);
CREATE TABLE senders (
	address       TEXT PRIMARY KEY,
	domain        TEXT NOT NULL,
	route_folder  TEXT NOT NULL,
	route_source  TEXT NOT NULL,
	mixed         INTEGER NOT NULL,
	sent_to_count INTEGER NOT NULL,
	reason        TEXT NOT NULL,
	updated_at    INTEGER NOT NULL
);
CREATE INDEX senders_folder ON senders(route_folder);
CREATE TABLE domains (
	domain         TEXT PRIMARY KEY,
	default_folder TEXT NOT NULL,
	senders        INTEGER NOT NULL,
	agreeing       INTEGER NOT NULL
);
CREATE TABLE audit (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	action      TEXT NOT NULL,
	message_key TEXT NOT NULL DEFAULT '',
	from_folder TEXT NOT NULL DEFAULT '',
	to_folder   TEXT NOT NULL DEFAULT '',
	reason      TEXT NOT NULL,
	at          INTEGER NOT NULL,
	undone      INTEGER NOT NULL DEFAULT 0
);
CREATE VIEW sender_folder_counts AS
	SELECT address, folder, COUNT(*) AS count, MAX(internal_date) AS last_seen
	FROM observations GROUP BY address, folder;
`,
	// 2: Message-IDs of mail you sent, to recognise replies to your own mail
	// (classification: "you're in the thread"). IDs only, never subjects.
	`
CREATE TABLE sent_message_ids (
	message_id TEXT PRIMARY KEY,
	sent_at    INTEGER NOT NULL
);
`,
	// 3: audit outcome and destination (for undo and crash reconciliation), and
	// per-folder UID cursors so run/once only look at new mail.
	`
ALTER TABLE audit ADD COLUMN status TEXT NOT NULL DEFAULT 'done';
ALTER TABLE audit ADD COLUMN dest_uidvalidity INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audit ADD COLUMN dest_uid INTEGER NOT NULL DEFAULT 0;
CREATE TABLE cursors (
	folder      TEXT PRIMARY KEY,
	uidvalidity INTEGER NOT NULL,
	next_uid    INTEGER NOT NULL
);
`,
}
