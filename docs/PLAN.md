# quietbox: plan

## Goal

Replace SaneBox for a personal mailbox with a self-hosted tool that works with **any IMAP
provider** (developed against Fastmail). It runs as a long-lived process on a NAS or VPS,
or as a one-shot command from cron.

## Non-goals (for now)

- LLM classification (planned later, behind the same `classify` interface)
- Web UI. All control happens through mail folders plus the CLI.
- Multiple accounts per process (run one instance per account; `--account NAME` makes that easy, see M3.1)
- Reading or storing message bodies. Headers are enough for v1.
- Snooze and reply reminders (see "Ideas")

## Core concept: typed folders

The user configures any number of folders. Each one has a **type**, and the type tells
quietbox what kind of mail belongs there, so it can route **senders it has never seen**.

```toml
[[folder]]
name = "INBOX"
type = "important"

[[folder]]
name = "SaneLater"
type = "later"

[[folder]]
name = "SaneNews"
type = "newsletter"

[[folder]]
name = "SaneBulk"
type = "bulk"          # receipts, notifications, transactional

[[folder]]
name = "SaneBlackHole" # optional
type = "blackhole"     # sender blocked; future mail goes to Trash
```

| Type         | Holds                                                   | Detection signals for unknown senders |
|--------------|---------------------------------------------------------|---------------------------------------|
| `important`  | people you correspond with                              | you have sent mail to them; thread you started or replied to; allowlist |
| `newsletter` | editorial content you subscribed to                     | `List-Id` + `List-Unsubscribe`, newsletter platforms (Substack, Beehiiv, Mailchimp, Buttondown…), regular cadence |
| `bulk`       | receipts, order/shipping updates, account notifications | `noreply@`/`notifications@` patterns, transactional ESP headers, subject keywords (receipt, order, invoice, ricevuta, ordine…), no `List-Id` |
| `later`      | everything else that isn't important                    | fallback for automated-looking mail matching neither of the above |
| `blackhole`  | blocked senders                                         | never assigned automatically, only by the user |

Rules:
- More than one folder per type is allowed, but then only training sends mail there,
  never detection (detection needs exactly one target per type). To opt in, set
  `detect = true` on exactly one folder of that type; `quietbox check` warns when a
  type has several folders and no detection target.
- Config validation (hard errors): exactly one `important` folder and it is `INBOX`;
  at least one `later`; at most one `blackhole`; no duplicate names; no unknown keys;
  `detect = true` is not allowed on `blackhole`. The IMAP password is never in the
  file: it comes from `[imap] password_file` / `password_command` (M3.2) or `QUIETBOX_IMAP_PASSWORD`.
- `[imap]` section: `host`, `port` (default 993 / 143), `username`, `tls`
  (`implicit` default, `starttls`, or `none`, which is only accepted for loopback hosts).
- A config with only `important` + `later` works fine (SaneBox's basic setup).
- **Unknown mail that matches nothing stays in INBOX.** Hiding a real person's email is
  the worst possible failure.

## First run: `quietbox init`

Idempotent: safe to re-run at any time as `quietbox learn`.

1. Connect and check server capabilities.
2. For each configured folder:
   - **missing:** create it (asks for confirmation; `--yes` to skip; respects `--dry-run`)
   - **exists:** scan message headers (From, Date, List-Id) and count messages per
     sender per folder
3. Scan **Sent** to record every address you have written to (strongest `important` signal).
4. Scan **INBOX** the same way, but only messages from the last `inbox_lookback` (default
   6 months), since old inbox mail may predate SaneBox.
5. Resolve each sender's route:
   - all mail in one folder: route = that folder
   - split across folders: majority over the last N messages, weighted toward recent mail,
     and the sender is marked **mixed** (see below)
   - you have sent mail to them: `important`, unless a non-inbox folder holds a clear majority
6. Derive **domain defaults**: when most known senders from a domain share a route,
   a new sender from that domain inherits it (e.g. a new `@substack.com` sender → news).
7. Print a summary and write `learn-report.csv`: senders per folder, mixed senders,
   overrides. You review it before anything moves.

Options: `--since 2y` to limit scan depth, `--folder SaneNews` to re-learn one folder.

### Mixed senders

Some senders legitimately belong in two folders (Amazon: receipts in `bulk`, deals in
`later`). A mixed sender follows its majority route. The idea of letting a strong per-message
detector match override it (e.g. "Your order has shipped" → `bulk`) is implemented as an opt-in
experiment (`eval --mixed-override`, `classify.Policy.MixedOverride`): on a real mailbox it was
right 1 time in 4 and hid INBOX mail, so it is off by default.

## Classification order (first match wins)

1. sender is blocked → `blackhole`
2. sender has a learned/trained route → that folder (a mixed sender follows its majority route)
3. you have sent mail to the sender, or you're in the thread → `important`
4. domain default → that folder
5. type detectors (`newsletter`, then `bulk`, then `later`)
6. no match → stays in INBOX

A learned sender route is checked before the thread rule, so a reply from a sender you have
filed away stays filed; `quietbox eval` counts the cost of that as "hidden important".
Every decision records its **rule**, **reason** and the detector **signals** behind it;
`quietbox explain <message>` (M4) shows them for one message.

## Ongoing training

Same as SaneBox: moving a message *into* a configured folder re-routes that sender there, and
moving one *back to INBOX* keeps that sender in INBOX. One move is enough and the last move wins.
User moves always override learned routes and detectors.
IMAP doesn't report moves, and quietbox does not diff folder snapshots: every typed folder has a
cursor like INBOX, and each pass reads only what **arrived** since. An arrival that quietbox did
not put there itself (the audit log says where its own moves landed) is a move by you. Details in
M4 below.

## State (SQLite)

- `folders(name, type, present, last_scanned_at)` (`present`: `exists` is an SQL keyword)
- `senders(address, domain, route_folder, route_source[learned|trained|detected], mixed, sent_to_count, updated_at, reason)`:
  a `trained` row (M4) is never replaced by `init`/`learn`; its reason says what trained it
- `observations(address, folder, internal_date)`: one row per message seen, no keys, subjects or bodies.
  Recency weighting needs per-message dates, and re-learning one folder replaces just that folder's rows.
  `sender_folder_counts` is a view over it.
- `sent_to(address, count, last_sent)`: recipients found in Sent
- `sent_message_ids(message_id, sent_at)`: Message-IDs of mail in Sent (schema v2), so a reply to
  your own mail is recognised as "in the thread". IDs only, never subjects.
- `domains(domain, default_folder, senders, agreeing)`
- `messages(key, folder, internal_date)` (schema v5): where a message was last seen in a typed folder, so a
  move back to INBOX is recognised. Filled by the `init`/`learn` scan of typed folders, by quietbox's own moves and
  by typed-folder arrivals; a row is deleted when the message returns to INBOX or `undo` restores it. Keys and
  folders only.
- `training(id, at, sender, folder, source[move|manual], message_key)` (schema v5): one row per training event,
  newest shown by `explain`. `folder` is the new route (the INBOX name means "stays in INBOX").
- `decisions(message_key, folder, reason, at)`
- `audit(id, action, message_key, from_folder, to_folder, reason, at, undone, status, dest_uidvalidity, dest_uid)`: `action` is `create_folder`, `move` or `undo_move`; `status` is `planned|done|failed`
- `cursors(folder, uidvalidity, next_uid, backlog_below)`: where `once`/`run` resume (INBOX and Sent from schema v3;
  every typed folder too from M4)

Message key: OBJECTID `EMAILID` → `Message-ID` header → hash(From, Date, Subject).

## Milestones

Each one ends with a demo against the docker test server and a tagged commit.

### M0: Skeleton, read-only
- repo layout, Makefile, CI, docker-compose test IMAP server with seeded mail
  covering every folder type, including mixed senders
- config loading + validation (types, one detection target per type)
- `quietbox check`: connect, list capabilities, show which configured folders exist
- Test server: Dovecot 2.4.5 (pinned by digest), seeded from `testdata/seed/` by `tools/seed`.
  It does **not** advertise OBJECTID, so local tests only cover the `Message-ID` key fallback;
  the `EMAILID` path is verified only against a server that supports RFC 8474 (Fastmail).
- **Done when:** works against the test server and your real Fastmail.

### M1: `init` / `learn`
- folder creation (the only mutation before M3): only with `--yes` or an interactive yes,
  audit row written first
- read-only header scanning (EXAMINE + `BODY.PEEK[HEADER.FIELDS]`, 1000 UIDs per FETCH),
  Sent + INBOX scan
- sender routes, mixed senders, domain defaults
- `quietbox senders [--folder X] [--mixed]` (database only), `learn-report.csv`
- **Done when:** on your real mailbox the report matches your intuition and the
  runtime is acceptable (measure it; batch FETCH of headers only).

Route resolution (`internal/learn`, thresholds in `[learn]`):
1. Take a sender's newest `window` (20) observations, weight each by `0.5^(age / half_life)`
   (`half_life` 90d); a folder's share is its weight over the total. Highest share wins; ties
   prefer INBOX, then name. Mixed when another folder's share is at least `mixed_share` (0.2).
2. Sent-to: INBOX, unless a non-INBOX folder has share at least `sent_override_share` (0.75)
   and at least 3 messages.
3. Domain default: at least `domain_min_senders` (3) senders and `domain_share` (0.8) of them on
   one route. Mailbox-provider domains (gmail.com, outlook.com, yahoo.*, ...) and
   `learn.personal_domains` never get one, and neither does the blackhole folder.
4. INBOX is only read back `inbox_lookback` (6mo). Your own addresses (`[me]`) are skipped, and so
   are messages without a parseable From. The same message twice in one folder counts once.

Message keys are not persisted yet. go-imap v2 (beta.9) cannot fetch EMAILID, so `mail.Key`
falls back to Message-ID, then the hash. **Prerequisite for M3:** fetch EMAILID before the audit
log starts storing keys.

### M2: Detectors + shadow mode (still read-only): implemented
- `internal/classify`: pure detectors and `Classify`. Everything is table-tested; the detector
  fixtures are **fabricated** header sets (several platforms and ESPs, English and Italian,
  plus person-to-person negatives), not real samples, so the real-mail check is `quietbox eval`.
  - Each detector returns a strength (`weak` = some evidence, `strong` = several independent
    signals) and the signals that fired. `strong` matters only for the opt-in mixed-sender override.
  - `newsletter`: `List-Id` (+ `List-Unsubscribe` = strong); a newsletter-platform domain in From
    or Return-Path (substack, beehiiv, buttondown, mailchimp, convertkit, ghost, mailerlite); or
    `List-Unsubscribe` without a `List-Id` (weak, strong with `Precedence: bulk|list|junk`). System
    senders (`noreply@`-style) never count as newsletters.
  - `bulk`: needs a `noreply`/`notifications`/`billing`/`orders`... sender; a transactional ESP
    header (SES, Postmark, SendGrid, Mailgun) or a subject keyword (receipt, order, invoice,
    shipped, ricevuta, ordine, fattura, spedizione...) makes it strong. ESP headers and keywords
    never fire on their own (ESPs carry plenty of marketing), and mail with a `List-Id` is never bulk.
  - `later`: `Precedence: bulk|list|junk`, `Auto-Submitted`, `List-Unsubscribe`, marketing ESP
    headers (`X-Campaign-Id`...), mass-mailer `X-Mailer`; two or more signals = strong. `Feedback-ID`
    only counts next to another signal.
  - A detector whose type has no detection target (see `detect = true`) cannot fire and the
    next one is tried. Own addresses and mail without a From are never moved.
- `quietbox scan`: what quietbox would do with INBOX mail and why; `--csv`, `--limit`, `--all`.
  It reads only and needs a previous `init`. (The plan called it `scan --report`; it is always a
  report in M2, and acting on the result is `once`/`run` in M3.)
- `quietbox eval` (hold-out evaluation): learn only from mail older than `--cutoff` (default 60
  days ago; INBOX lookback and Sent are measured from the cutoff), classify mail received after
  it, and compare with where it actually is. Known senders agree almost by construction, so the
  report separates **new senders** (no history before the cutoff): that number measures the
  detectors. It also prints a confusion matrix, agreement per rule, and **hidden important**
  (INBOX mail quietbox would have filed away: the failure to watch). It also splits the
  disagreements into hidden / leaked / cross-filed, attributes hidden-important to rules (and mixed
  senders), and prints agreement per rule x strength and the commonest new-sender signal
  combinations (fixed vocabularies only, so nothing personal). `--csv` lists the disagreements.
  In memory only: no database, nothing written to the mailbox.
- The first revision of these rules came from a real-mailbox `eval` (139 new-sender messages):
  `List-Unsubscribe` alone went to SaneNews 10 of 10, ESP-only mail was almost never Bulk, a
  `noreply`-style sender was Bulk 13 of 18, and `Feedback-ID` alone was noise. The test fixtures
  remain fabricated; the real check is always `eval` on a mailbox.
- `eval` experiments are never defaults and the report names them. Today there is one,
  `--mixed-override` (see "Mixed senders"). Two others were measured on a real mailbox and
  removed: `--thread-first` (no hidden message was a reply to mail you sent, so it changed
  nothing) and `--only-strong` (hidden stayed the same, "leaked" went from 42 to 67).
  `classify.Policy` carries the experiments. For the hidden-important mail from known senders
  the report says how many were replies, how many replied to mail you sent, route-share and
  history-size buckets, and how many were mixed senders. A "Thread check" line shows how many
  sent Message-IDs were learned and how many test messages reply to one, as a sanity check that
  thread matching works on real mail.
- First real-mailbox result with the revised detectors (cutoff 2026-09-20, 813 test messages):
  agreement 89%, new senders 69% (was 46%), cross-filed 76 -> 21, hidden important 30 of 311 INBOX
  messages (25 from consistently-filed senders in a messy INBOX; a spot check found most of them
  correctly filed), "leaked" 42 (mild: unknown senders left in INBOX).
- Message headers now also carry `In-Reply-To`, `References` and a whitelist of automation
  headers (`Signals`). IMAP `HEADER.FIELDS` has no wildcards, so ESP markers are listed by name.
- **Done when:** new-sender agreement on your real mailbox is good enough for you to trust it.
  This part cannot be verified without your mailbox: run `quietbox eval` on it.

Known limitation: `init` learns a route of INBOX for every sender who has mail in INBOX (that is
what the history says), so `scan` on a mailbox that `init` has just read will mostly show
"sender route to INBOX". Detectors matter for senders that appear after `init`. Whether an
INBOX-only history should count as a learned route is something to decide from `eval` numbers.

### M3: Run for real: implemented
- `quietbox once` (cron), `quietbox run` (daemon, IDLE on INBOX), `--dry-run`, `quietbox undo --since 1h`.
  Design: `docs/superpowers/specs/2026-11-20-m3-run-design.md`; plan: `docs/superpowers/plans/2026-11-20-m3-run.md`.
- **New mail only.** `init` records a cursor per folder for INBOX and Sent: `(UIDVALIDITY, next UID)`, taking
  UIDNEXT *before* the scan so mail arriving during `init` is not lost. `once`/`run` fetch only `UID >= cursor`.
  If UIDVALIDITY changed, nothing moves from that folder: the cursor is reset and the user is told to re-run `init`.
  (Typed folders got cursors in M4, see there.) Both commands refuse to run before `init`.
- **One pass (`engine.Process`)**, shared by `once` and `run`: lock file (`<state path>.lock`, flock, Unix only;
  `--dry-run` takes none) -> require `MOVE` + `UIDPLUS` -> settle `planned` audit rows left by a crash (found in
  the destination by Message-ID -> `done`, else `failed`) -> check UIDVALIDITY -> Sent (new recipients and
  Message-IDs are added, never replaced; a dry run applies them in memory only, so it decides like the real run)
  -> INBOX.
- **INBOX step.** Skipped: `\Seen` and `\Flagged` mail (`[run] skip_seen`, `skip_flagged`, default true) and any
  message whose key already has a `move` audit row from an earlier pass, whatever its status and even if
  undone (so a message dragged back to INBOX, or restored by `undo`, is never moved again). Everything else goes
  through `classify.Classify`; messages that leave INBOX are moved one by one: audit row `planned` -> `UID MOVE`
  -> row `done` with the destination UIDVALIDITY/UID from COPYUID (or `failed`; a failure is not retried and the
  pass continues). The cursor advances after the batch. A dry run prints the plan and writes and advances nothing.
  Two copies of one message delivered in the same pass are both filed (the guard knows earlier passes only).
- **Server requirements.** `MOVE` (RFC 6851) and `UIDPLUS` are required; quietbox never COPY+delete+expunges.
  go-imap's `Client.Move` falls back to exactly that when MOVE is missing, so the backend checks first
  (`moveGuard`) and the engine refuses before fetching anything. Not verified: which of Gmail/Outlook advertise
  both (run `quietbox check`); Outlook may also need OAuth2, which is unsupported.
- **`run`.** `Process`, then IDLE (re-issued every 20 min), repeated; a connection error closes the connection and
  reconnects with backoff (5s doubling to 5 min, reset by a clean pass). Refusals (no MOVE, no `init`, lock held,
  no Sent folder) are fatal. SIGINT/SIGTERM stop it after the current pass. It holds the lock for its lifetime, so
  `once` and `undo` while `run` is up fail with "another quietbox". Before every wait it compares the folder's
  UIDNEXT with the cursor and goes round again if mail arrived since the pass looked (the server would not announce
  it twice); the cursor therefore never ends a pass below the UIDNEXT read at its start.
- **`undo --since <duration> [--dry-run]`.** Takes `done`, not-undone moves in the window, newest first, finds each
  message by (UIDVALIDITY, UID) from COPYUID, or by Message-ID if UIDVALIDITY changed (hash keys cannot be searched:
  reported as unfindable), writes an `undo_move` row first, moves it back and marks the original undone. A message
  you moved yourself is skipped and reported. Takes the lock.
- **State.** Schema v3: `audit.status` (`planned|done|failed`), `audit.dest_uidvalidity`, `audit.dest_uid`, and
  `cursors(folder, uidvalidity, next_uid)`. The audit log holds keys, folders and reasons, never addresses or subjects.
- Message keys are still Message-ID then hash: go-imap v2 (beta.9) cannot fetch EMAILID.
- Blackhole: mail from a sender routed to the blackhole folder is moved *into that folder* like any other route;
  the "future mail goes to Trash" behaviour is M5.
- Verified against the Dovecot test server only (`make itest`: once, dry-run, undo, drag-back, run waking on
  delivery, lock). **Done when:** SaneBox is switched off and quietbox has run for a week. That part is not verified.

### M3.1: Accounts and fresh accounts: implemented
- Design: `docs/superpowers/specs/2026-11-21-accounts-and-fresh-design.md`; plan: `docs/superpowers/plans/2026-11-21-accounts-and-fresh.md`.
- Multiple accounts stay **one process per account**. `--account NAME` selects `<config dir>/NAME.toml` (config dir:
  `$XDG_CONFIG_HOME/quietbox` or `~/.config/quietbox`); it excludes an explicit `--config`. With an account the
  password is `QUIETBOX_IMAP_PASSWORD_<NAME>` then `QUIETBOX_IMAP_PASSWORD`, and the default database is
  `<state dir>/NAME.db`; `--config` keeps `quietbox.db`. `quietbox accounts` lists the config folder.
- Schema v4: `meta(key, value)` holds the account identity (`username@host`, lower-case); every command that opens a
  database binds it (`openState`): an unbound database adopts the account, another account is refused
  (`ErrWrongAccount`, naming both and `[state] path`). `cursors.backlog_below` marks a backlog sweep.
- `init --fresh [--backlog DUR]` (`--backlog` needs `--fresh`; `--fresh` excludes `--folder`): INBOX history teaches
  no routes (Sent and typed folders still do) and INBOX is read only for the backlog window. The INBOX cursor starts
  at the first message that arrived within DUR (else at the end) and `backlog_below` is the UIDNEXT read before
  that scan. `Process` does not apply `skip_seen` below the boundary (flagged mail, audited keys and own mail are
  still skipped) and clears the boundary once the cursor passes it. `init` moves nothing; its summary and
  `--dry-run` state how many INBOX messages the backlog covers. Ignoring `skip_seen` for the backlog is a design
  call, not a requirement.
- Dovecot integration tests use throwaway users (any user with the test password gets an empty mailbox).
- Not verified: Gmail/Fastmail. Unchanged: M3's "SaneBox off, one week" criterion.

### M3.2: Password sources: implemented
- Design: `docs/superpowers/specs/2026-11-21-password-sources-design.md`.
- `[imap] password_file` (path; `~/`, `$VAR`/`${VAR}` expanded, an unset variable is an error naming it; relative
  paths resolve against the config file's folder; symlinks followed; one non-empty line) or `password_command`
  (`sh -c`, 30 s, one non-empty line; failures show exit status and stderr, never stdout). Both set is a config
  error; either wins over the environment variables, which still work when neither is set. A `password` key stays
  forbidden.
- A password file readable by group/others (`mode & 077`) is refused outside a container ("chmod 600 PATH") and
  warned about on **every** command inside one (`config.PasswordWarnings`, printed by `options.loadConfig`).
  Container: `QUIETBOX_CONTAINER` (`0`/`false`/`no`/`off` = no, any other value = yes) else `KUBERNETES_SERVICE_HOST`, `/.dockerenv` or
  `/run/.containerenv`. A heuristic: the override exists for runtimes it misses.
- Parsing stays pure; only `Load`/`LoadAccount`/`RefreshPassword` touch files and commands, and offline loads
  (`senders`, `accounts`) never do. `run` calls `RefreshPassword` before every (re)connect.
- Verified: unit tests with injected filesystem/command/environment, real file and real command tests, Dovecot
  integration tests (file, command, wrong password never echoed, `run` recovering after the file is corrected).
  Not verified: a real keychain/`pass`/systemd credential setup.

### M4: Training via folder moves: implemented
- Design: `docs/superpowers/specs/2026-11-21-m4-training-design.md`; plan: `docs/superpowers/plans/2026-11-21-m4-training.md`.
- **Arrivals only.** No snapshot diff and no departures: `init` gives every configured typed folder a cursor
  (UIDNEXT read before the scan, as for INBOX), and each pass fetches only `UID >= cursor` (not at all when the
  folder's UIDNEXT has not passed the cursor). Pass order: lock, MOVE check, reconcile, Sent, **typed folders** (config order), INBOX. Training runs before INBOX classification, so
  the mail that wakes `run` is already filed by what you just taught it.
- **File event.** A message that arrives in a typed folder re-routes its sender there (`route_source = trained`),
  unless quietbox put it there itself, or it is from one of your own addresses, or it has no parseable From. A
  move between two typed folders is a file event too. A Sieve rule delivering into a typed folder trains the same
  way (indistinguishable from a move).
- **Own moves are matched by destination UID.** An arrival is quietbox's own when a `done` `move` or `undo_move`
  audit row landed in that folder with the same UIDVALIDITY and UID (COPYUID). Rows without a destination UID fall
  back to key + folder. So a message you drag back and forth is still your move, and `undo` never trains.
- **Return event.** A message that arrives in INBOX and was last seen in a typed folder pins its sender to INBOX (a
  trained route whose folder is INBOX, reason "sender stays in INBOX"). It counts as a return only if its
  INTERNALDATE is **not after** the date stored for it (the `messages` row, or, without one, the time of
  quietbox's own move): a move keeps INTERNALDATE, a fresh delivery does not. Returns are never classified or
  moved, whatever their flags (`skip_seen` does not stop them training), and are forgotten from `messages`.
- **Known but not a return: left in INBOX.** An INBOX arrival whose key is in `messages` (or has a `done`,
  not-undone move of quietbox's) but is not a return is left alone: not classified, not moved, not trained,
  counted as skipped; its `messages` row stays. So a later copy (same Message-ID, e.g. a list cross-post) of any
  message quietbox has seen in a typed folder, filed by quietbox or seen there by `init`, stays in INBOX
  unclassified. On a server (or client) that does not preserve INTERNALDATE on a move, a drag back to INBOX is
  not learned, but the message is never moved out of INBOX again.
- **Moves whose outcome was not recorded.** A `move` or `undo_move` row left `planned` or `failed` (a crash, or a
  connection dropped after the server moved) explains its key in its destination folder, so it never trains;
  a user move of that same message into that same folder is missed instead. Reconcile settles `planned`
  `undo_move` rows like moves (found: done, original move undone, message forgotten; else failed).
- **Last move wins.** Events in one pass apply in processing order (typed folders in config order, then INBOX,
  UIDs ascending); if they disagree for one sender the last wins and a note says so. An event equal to the
  sender's current trained route writes nothing. A trained sender is no longer `mixed`. Domain defaults are
  untouched.
- **Notes vs warnings.** `ProcessReport.Notes` (printed `note: ...`) are information only and never make `once`
  exit non-zero; `Warnings` still do. `once`/`run` print `trained <sender>: you moved a message ...` per event and
  `trained N sender(s) from your moves`; `--dry-run` prints `would train` and writes nothing.
- **Existing M3 databases.** A migration cannot know where typed folders ended. On the first M4 pass a typed folder
  without a cursor gets one at its current UIDNEXT, with the note "training from moves starts now for ...; earlier
  moves are not learned" (a dry run skips such folders). `messages` is empty on such a database, so until
  `init`/`learn` is re-run only messages quietbox filed itself (found through the audit log) are recognised when
  you move them back to INBOX; messages you filed by hand before are not.
- **`senders set <addr> <folder>` / `senders unset <addr>`.** `set` writes a trained row (reason "set by hand") and
  a `manual` training row; the folder must be a configured folder (INBOX allowed, Sent not) and the address must
  contain `@`, both checked before waiting for the lock. `unset` deletes a
  trained row only; a learned route the training had replaced comes back at the next `init`/`learn`, not before.
  Both work on the database (no IMAP login) and **wait for the instance lock** ("waiting for another quietbox..."
  after a second, Ctrl-C stops waiting) instead of failing. Because `run` holds the lock for its whole lifetime,
  while a daemon is up they wait until it stops: stop `run`, set, start it again.
- **`explain [--folder F] <uid | message-id>`.** Opens the folder read-only (default INBOX), fetches that one
  message's headers and prints what `Classify` would decide *now*: decision, rule, reason, signals, the sender's
  route and source, its latest training event and the audit rows for the message key. Sender address, UID and
  Message-ID only; never the subject or body; times in local time with the zone. UID 0 or an empty argument is a
  usage error before connecting. Takes no lock, writes nothing.
- No config switch turns training off (on purpose); `senders unset` is the escape hatch.
- **State.** Schema v5: `messages(key, folder, internal_date)`, `training(...)`, cursors for typed folders.
- Verified against the Dovecot test server only (`make itest`: filing by hand trains, drag back keeps the sender in
  INBOX, `undo` is not training, `senders set`/`unset` including `set` waiting while `run` is up, `explain`
  output without the subject, `run` learning from a move on the next delivery) and a manual demo.
- **Not verified:**
  - real Fastmail or Gmail. That INTERNALDATE is preserved on MOVE (which return detection relies on) is verified
    on Dovecot only; on Gmail/Fastmail it is unverified.
  - duplicate-copy behaviour (a later copy of a known message stays in INBOX) is unit-tested with the fake backend
    only.
  - scale: the audit log and the whole `messages` table are read once per pass (measured by the reviewer: about
    100 ms and 75 MB at 200k `messages` rows), and `messages` rows are not pruned when mail leaves a typed folder
    other than to INBOX; hundreds of moves in practice.
  - `init --since` limits what `messages` remembers, so older hand-filed mail dragged back to INBOX is not
    recognised as a return: it has no `messages` row and no move of quietbox's, so it is not learned, and it is
    classified like new mail and may be filed again (if unread, or any message when `skip_seen` is off).
  - a second copy with the same Message-ID and the same INTERNALDATE second, seen one pass later, reads as a
    return (rare).
  - a message present in a typed folder and in INBOX in one pass (a Gmail label without archive, Sieve
    `fileinto :copy`) trains twice and ends "stays in INBOX", with a "moves disagreed" note.
  - `set`/`unset` contention with `run` beyond the one integration test; the M3-database upgrade path on a real
    mailbox (unit-tested with the fake backend only).

### M5: BlackHole
- `blackhole` folder type: move a message there to block the sender

### M6: Open-source release
- Docker image, goreleaser binaries, README (Fastmail, Gmail, generic IMAP; app
  passwords), example configs incl. a "migrating from SaneBox" guide, LICENSE

## Ideas (not planned)

- Snooze folders, reply reminders via plus addressing
- JMAP backend (Fastmail, Stalwart) for push and stable IDs
- Local LLM for low-confidence decisions
- Daily digest email of what was filed away
- More types (`receipts` split from `bulk`, `social`, `calendar`)

## Open questions

- Should `init` read Fastmail contacts (CardDAV) as an allowlist, or just a vCard file?
- Inbox lookback default: is 6 months right for your mailbox?
