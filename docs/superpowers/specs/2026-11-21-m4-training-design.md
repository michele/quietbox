# M4: Training via folder moves: design

Status: implemented (see `docs/PLAN.md`, M4). Builds on M3 (`once`/`run`/`undo`), see `docs/PLAN.md`.

## Intent

For years the user's main way of teaching SaneBox was to move mail by hand: into a folder to
say "this sender belongs there", back to INBOX to say "this sender stays in the inbox". They
did the second one hundreds of times. quietbox must learn from both gestures, immediately, with
one move being enough. User moves always beat learned routes and detectors.

Also in M4: `quietbox explain <message>` (why does this message go where it goes) and
`quietbox senders set <addr> <folder>` (train by hand).

## Goals

- A message that **arrives** in a configured typed folder and was not put there by quietbox
  re-routes its sender to that folder (`route_source = trained`).
- A message that arrives in INBOX and is known to have been in a typed folder pins its sender
  to INBOX (a trained route whose folder is INBOX, meaning "stays").
- Last move wins, whether to a folder or to INBOX. One move is enough; no threshold.
- Training happens before classification in the same pass, so a sender you just filed is never
  mis-filed by the mail that woke the daemon.
- `explain` shows the rule, reason, signals and the training event behind a decision.

## Non-goals

- Departures. Nothing watches a message leaving a folder; every move produces an arrival
  somewhere, and arrivals are all we read.
- Domain-level training. A move changes the sender route only; domain defaults are untouched.
- Retroactive training for moves made before M4 was installed.
- Telling a user move from a server-side (Sieve) delivery into a typed folder. Both train.
- Deleting or trashing a message is not a signal.
- The blackhole folder (M5). Until then a blackhole folder is trained like any typed folder.

## Anti-goals

- quietbox must never train from its own moves (a feedback loop), nor from `undo`.
- It must never move a message you put somewhere by hand.
- Training never mutates the mailbox. It changes local state only.

## Constraints

- `classify` and `learn` stay pure. Detection of moves is engine work; `classify` gets no new
  inputs.
- No new dependency. Dovecot test server, `MOVE` + `UIDPLUS` as in M3.
- Message key stays Message-ID, then hash (go-imap v2 beta.9 cannot fetch EMAILID).
- Audit log and training tables hold keys, folders and sender addresses, never subjects or bodies.

## Approaches considered

- **A. Full snapshot diff each pass.** Lists every UID in every folder. Complete, but cost grows
  with mailbox size on every IDLE wake. Rejected.
- **B. Arrivals only (chosen).** Typed folders get cursors like INBOX and Sent; each pass fetches
  only UIDs at or above the cursor. A `messages(key, folder)` table says where a message was
  last seen. Chosen by the user: "we don't care about departures, only arrivals".
- **C. Re-run `learn` on typed folders.** One move only nudges counts, so "last move wins" and
  "one drag is enough" fail. Rejected.

## Design

### Reading arrivals

Pass order in `engine.Process`: lock, MOVE check, reconcile, UIDVALIDITY checks, Sent, **typed
folders**, INBOX. Typed folders come before INBOX classification.

1. **Typed folders.** For each configured typed folder, `checkCursor`, then `FetchFrom(cursor)`.
   Each fetched message is an *arrival*. Skip it if the audit log explains it (below) or if its
   From is one of your own addresses or unparseable. Otherwise it is a **file event**: sender ->
   this folder.
2. **INBOX.** Each fetched message whose key is in `messages` (so it was last seen in a typed
   folder) and whose INTERNALDATE is **not after** the date stored there, or whose key has a
   `done`, not-undone `move` audit row made at or after the message's INTERNALDATE (fallback for
   databases without `messages` rows), and whose arrival the log does not explain, is a
   **return event**: sender -> INBOX. A move keeps INTERNALDATE, a fresh delivery does not, so a
   second copy of a known message (same Message-ID, delivered later, e.g. a list cross-post) is
   never a return and never pins the sender. **Known but not a return** (as built, ruled in the
   final review): an INBOX arrival whose key is in `messages` or has a `done`, not-undone `move`
   row, but which is not a return, is **left in INBOX**: not classified, not moved, not trained,
   counted as skipped, and its `messages` row stays as it is. This covers a later copy (same
   Message-ID) of any message quietbox has seen in a typed folder, whether quietbox filed it or
   `init` saw it there, and a drag back on a server (or client) that stamps a new INTERNALDATE on
   a move: such a drag back is not learned, but the message is never moved out of INBOX again.
   Return events are handled before the `skip_seen`
   / `skip_flagged` checks, because a dragged-back message is usually already read. The message
   is never classified or moved. All other INBOX mail is classified as in M3.
3. **Apply.** File and return events update `k.Senders` in memory (so step 2's classification
   uses them) and are persisted by a real run. A sender with a trained route is no longer
   `mixed`. If one pass holds conflicting events for the same sender, the later one in processing
   order wins (typed folders in config order, then INBOX, UIDs ascending) and a note (information
   only; it never makes `once` fail) says so.

**"The audit log explains it".** An arrival is quietbox's own if an audit row (`move` or
`undo_move`, status `done`) has this folder and `dest_uidvalidity`/`dest_uid` equal to the
arrival's. Matching on the destination UID, not the key, means a message the user drags back and
forth between folders is still seen as a user move. For rows without a destination (reconciled
`planned` rows), fall back to key plus folder. Moves made by `undo` are explained the same way.
A `move` or `undo_move` row whose outcome is `planned` or `failed` may still have happened (a
crash, or a connection dropped after the server carried out the MOVE), so it too explains its key
in its destination folder: a user move of that same message into that same folder is missed,
which is far cheaper than a false trained route. Reconcile settles `planned` `undo_move` rows like
moves (found back in place by Message-ID: done, the original move marked undone and the message
forgotten from `messages`; otherwise failed), and a dry run reads `planned` rows of both actions
as reconcile would.

### Classification

`classify` needs almost no change. A trained INBOX route is a `SenderRoute{Folder: Inbox}`;
`Classify` already returns `Decision{Folder: Inbox, Rule: RuleSender}` for it, and `Process`
leaves messages with `d.Folder == inbox` alone. What is new:

- reason text for an INBOX route reads "you keep this sender in INBOX" (trained) so `explain` is
  clear;
- a table test: trained INBOX route beats a strong newsletter detector and a domain default, and
  a trained folder route beats the thread/sent-to rule as learned routes already do.

### State (schema v5)

- `cursors` rows for every configured typed folder, created by `init` (UIDNEXT read before the
  scan, as for INBOX and Sent).
- `messages(key PRIMARY KEY, folder, internal_date)`: the typed-folder messages quietbox knows
  about, filled by the `init`/`learn` header scan of typed folders (it already fetches the
  headers), by our own moves, and by typed-folder arrivals. A message that returns to INBOX
  (or is restored by `undo`) has its row deleted. No subjects or addresses.
- `training(id, at, sender, folder, source, message_key)`: `source` is `move` or `manual`;
  `folder` is the new route (INBOX name for "stays"). One row per event, for `explain`.
- `senders.reason` for a trained row says what trained it ("moved from INBOX to Newsletters on
  2026-11-21").
- **Existing M3 databases.** A migration cannot know the UIDNEXT of the typed folders. On first
  M4 pass a typed folder with no cursor gets one at its current UIDNEXT, with a one-line notice
  ("training from moves starts now; earlier moves are not learned"). `messages` is empty for such
  databases, so only moves of messages quietbox itself filed (found through the audit log) are
  recognised as returns until `init`/`learn` is re-run.

### Commands

- `once`, `run`: unchanged flags. The report adds "trained N sender(s)" and lists each event
  (sender, from -> to). `--dry-run` shows the events it would record and writes nothing.
- `senders set <addr> <folder>`: folder must be a configured typed folder or the INBOX name.
  Writes a trained row and a `manual` training event. `senders unset <addr>` removes a trained
  row; the sender's learned route (if any) comes back at the next `learn`/`init`, because
  training replaces the learned row. Both work offline against the database. **Both take the
  instance lock and wait for it** (decided by the user) instead of failing like `undo` does: they
  print "waiting for another quietbox..." after a second and honour Ctrl-C. Caveat: `run` holds
  the lock for its whole lifetime (M3), so while a daemon is up they wait until it stops.
- `explain`: `quietbox explain [--folder F] <uid | message-id>`. Connects read-only, fetches the
  one message's headers, runs `Classify` with current knowledge and prints rule, reason, signals,
  the sender's route and its training event, and any audit rows for the message key. It never
  mutates anything. It shows what the classifier would decide *now*.
- `run`: nothing new. It still IDLEs on INBOX only. A typed-folder move is noticed on the next
  pass, which any INBOX delivery triggers; since training runs before classification, latency
  cannot mis-file mail.

### Safety

- No mailbox mutation, so no new audit actions. Training rows are the audit trail for route
  changes.
- Nothing is logged beyond sender addresses already in the database; no bodies, no credentials.
- Dry runs take no lock and write nothing, as in M3.

## Testing

- Unit: `classify` table cases above; engine tests with the fake backend for every arrival case:
  user file event, own move (not training), `undo` (not training), return of our move, return of
  a pre-`init` message (via `messages`), drag A -> INBOX -> A, conflicting events in one pass,
  own-address arrival, `skip_seen` return still trains, hash-key and Message-ID-key messages,
  missing cursor on an M3 database, UIDVALIDITY change on a typed folder, dry run writes nothing.
- Store: migration v4 -> v5, cursor creation, `ReplaceSenders` keeps trained rows (exists).
- Integration (Dovecot, `make itest`): a second IMAP connection plays the user. INBOX -> folder
  then `once` then the sender's next message is filed; folder -> INBOX then next message stays;
  `senders set`/`unset`; `explain` output; `run` waking on delivery after a user move.
- Done = CLAUDE.md's definition: tests with `-race`, `make lint`, demo against the Dovecot test
  server with the output shown, `docs/PLAN.md` M4 section and README updated.

## Left to the builder (builder's calls, flagged)

- A blocking lock acquire for `senders set`/`unset` (the existing `AcquireLock` fails fast; add a
  waiting variant that takes a context).
- How `explain` fetches one message: `Locate` for a Message-ID, and for a UID a fetch from that
  UID filtered to it, unless a one-UID backend call is cheap to add.
- Whether `audit` already stores `dest_*` for `undo_move` rows; if not, add them in v5.
- Exact wording of report lines and warnings.
- No config switch to turn training off (kept out on purpose; `senders unset` is the escape).
