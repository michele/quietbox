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
name = "+QuietLater"
type = "later"

[[folder]]
name = "+QuietNews"
type = "newsletter"

[[folder]]
name = "+QuietBulk"
type = "bulk"          # receipts, notifications, transactional

[[folder]]
name = "+QuietBlackHole" # optional
type = "blackhole"     # sender blocked; mail stays here for [blackhole] retention, then goes to Trash
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
7. Print a summary and write the CSV report next to the database (`quietbox.learn-report.csv`,
   `<account>.learn-report.csv`; `--report` overrides): senders per folder, mixed senders,
   overrides. You review it before anything moves.

Options: `--since 2y` to limit scan depth, `--folder +QuietNews` to re-learn one folder.

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
- `blackhole_seen(key, first_seen)` (schema v6): when the retention sweep first saw each message in the blackhole
  folder; keys and times only. The time of the last sweep is `meta.blackhole_swept_at`.
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
- `quietbox senders [--folder X] [--mixed]` (database only), the learn report (`<db name>.learn-report.csv`)
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
- **One pass (`engine.Process`)**, shared by `once` and `run`: pass lock (`<state path>.lock`, flock, Unix only;
  `--dry-run` takes none; see M4.1 for how `run` holds it) -> require `MOVE` + `UIDPLUS` -> settle `planned` audit rows left by a crash (found in
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
  reconnects with backoff (5s doubling to 5 min, reset by a clean pass). Refusals (no MOVE, no `init`, another
  `run` holds the daemon lock, no Sent folder) are fatal. SIGINT/SIGTERM cancel its context: a wait (IDLE, backoff,
  the pass lock) ends at once; a pass stops at its next check (between messages, and at the start of each move or
  lookup), while an IMAP command already sent runs to completion. A move left between its `planned` row and the
  server's answer is settled by `reconcile` at the next pass (of `run` or `once`). Since M4.1 it holds its own daemon lock
  for its lifetime (a second `run` is refused) and the pass lock only while a pass executes, so `once` and `undo`
  work while it idles. Before every wait it compares the folder's
  UIDNEXT with the cursor and goes round again if mail arrived since the pass looked (the server would not announce
  it twice); the cursor therefore never ends a pass below the UIDNEXT read at its start.
- **`undo --since <duration> [--dry-run]`.** Takes `done`, not-undone moves in the window, newest first, finds each
  message by (UIDVALIDITY, UID) from COPYUID, or by Message-ID if UIDVALIDITY changed (hash keys cannot be searched:
  reported as unfindable), writes an `undo_move` row first, moves it back and marks the original undone. A message
  you moved yourself is skipped and reported. Takes the pass lock (waiting at most `--lock-timeout`, M4.1).
- **State.** Schema v3: `audit.status` (`planned|done|failed`), `audit.dest_uidvalidity`, `audit.dest_uid`, and
  `cursors(folder, uidvalidity, next_uid)`. The audit log holds keys, folders and reasons, never addresses or subjects.
- Message keys are still Message-ID then hash: go-imap v2 (beta.9) cannot fetch EMAILID.
- Blackhole: mail from a sender routed to the blackhole folder is moved *into that folder* like any other route;
  mail then stays there for `[blackhole] retention` before it goes to Trash (M5).
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
  Both work on the database (no IMAP login) and **wait for the pass lock** ("waiting for another quietbox..."
  after a second, Ctrl-C stops waiting) instead of failing. Since M4.1 `run` holds that lock only while a pass
  executes, so with a daemon up they wait at most one pass, and its next pass uses the new route.
- **`explain [--folder F] <uid | message-id>`.** Opens the folder read-only (default INBOX), fetches that one
  message's headers and prints what `Classify` would decide *now*: decision, rule, reason, signals, the sender's
  route and source, its latest training event and the audit rows for the message key. Sender address, UID and
  Message-ID only; never the subject or body; times in local time with the zone. UID 0 or an empty argument is a
  usage error before connecting. Takes no lock, writes nothing.
- No config switch turns training off (on purpose); `senders unset` is the escape hatch.
- **State.** Schema v5: `messages(key, folder, internal_date)`, `training(...)`, cursors for typed folders.
- Verified against the Dovecot test server only (`make itest`: filing by hand trains, drag back keeps the sender in
  INBOX, `undo` is not training, `senders set`/`unset` including `set` while `run` is up (M4.1), `explain`
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
  - the M3-database upgrade path on a real mailbox (unit-tested with the fake backend only).

### M4.1: `run` releases the lock between passes: implemented
- Design: `docs/superpowers/specs/2026-11-21-run-idle-lock-design.md`; plan: `docs/superpowers/plans/2026-11-21-run-idle-lock.md`.
- **Two locks, one order.** The daemon lock `<state path>.lock.run` is taken only by `run`, at start, fail-fast
  ("another quietbox run is already running on this mailbox (lock file ...)"), and held until it exits. The pass
  lock `<state path>.lock` (`LockPath`) is taken by `run` around each `Process` pass (waiting for it, never failing)
  and by `once`, `undo`, real `init`/`learn` and `senders set`/`unset`. Order: daemon lock, then pass lock; nothing
  takes them the other way round. The pass lock is never held across the IDLE wait, the "mail waiting?" check or a
  reconnect backoff. Dry runs, `scan`, `eval`, `explain`, `check` and `senders` (list) take no lock.
- **Why there is no race.** Every pass re-reads its state from SQLite (routes, cursors, audit log, `messages`), so
  what another command did in the gap is seen. Moves made by `once`/`undo` in the gap are explained by their audit
  rows (destination UID; `undo_move` rows), so the daemon neither moves a message twice nor trains on them.
  `reconcile` of `planned` rows runs under the pass lock. SQLite is in WAL mode with a 5 s busy timeout, so the
  daemon's unlocked UIDNEXT-vs-cursor read never conflicts with a writer.
- **Waiting.** `once`, `undo` and `init`/`learn` wait at most `--lock-timeout` (default `2m`, `engine.DefaultLockTimeout`;
  Go durations such as `90s`, `2m`, `1m30s`, and the config notation `1h`, `1d`; must be positive), print
  "waiting for another quietbox..." once after a second, then fail with "another quietbox is still running on this
  mailbox after waiting <d> (lock file ...)". A dry run never waits. `senders set`/`unset` wait without a bound
  (Ctrl-C). `run` waits for the pass lock without a bound, with its signal context (SIGINT/SIGTERM stop it while
  waiting); it says "waiting for another quietbox... (lock file ...)" after a second and "still waiting for the pass
  lock after <d> (lock file ...)" every 5 minutes (`engine.DefaultLockNotice`) for as long as the wait lasts. At
  the timeout a bounded wait tries the lock once more, so a lock freed in the last poll interval is taken.
- **Real `init`/`learn` hold the pass lock for the whole call**, including the interactive "create folders?" question
  and a long full scan. Meanwhile a daemon's passes and `senders set` wait, and a cron `once` can time out. The README
  recommends `--yes` or stopping `run` for a big `init`.
- **Process death.** `flock(2)` locks belong to the open file and the kernel drops them when the process exits for
  any reason (`kill -9` included). No PID files, timestamps or liveness checks; the empty lock files left on disk are
  harmless. `[state] path` should be on a local filesystem: `flock` on network filesystems can be unreliable.
- Verified: unit tests (daemon lock fail-fast, bounded wait and cancel, a child process holding both locks killed with
  SIGKILL, `run` holding no pass lock while idle) and Dovecot integration tests (`make itest`: `senders set` completes
  while `run` idles and the same daemon files the next delivery by the new route; a second `run` refused; `once` and
  `undo` while `run` idles, one `move` audit row per message, the daemon neither re-files nor trains on the undo; a
  5 s stress of `run` + a `once` loop + a `senders set`/`unset` loop + deliveries under `-race`: every message in
  exactly one folder, no key moved twice, no `planned` row; the real `run` in a child process killed with SIGKILL
  while idle and while waiting for the pass lock, after which `once` and a new `run` start at once) and a manual demo.
  Final wave: unit tests for the repeated "still waiting" notice (and once-only for the other commands) and the last
  attempt at the timeout; the stress test also checks that the daemon files mail delivered after the contention stops (the passes it wins *during* the stress are only reported: the lock is not fair, and on a slow or single-CPU machine it can win none), that flip@'s mail was
  filed only by its hand-set route (or stayed in INBOX with no move row) and that the last `set`/`unset` is the
  stored route.
- **Not verified:**
  - `flock` behaviour on network filesystems (NFS, SMB); documented as unsupported, not detected.
  - the kill -9 tests are Unix-only (like the locks); a kill in the middle of a pass is not tested end to end (a
    pass interrupted by a crash is settled by `reconcile`, M3). Windows remains unsupported.
  - lock fairness: the pass lock is polled every 200 ms, so a tight loop of lock takers (the stress test's
    `senders set` loop) can delay a waiting `once` (seen: about 3 s); it is bounded by `--lock-timeout`. The
    daemon's own wait for the pass lock is not bounded: a hung `init` (at its question) or a suspended `once` stalls
    its passes for as long as they live. That is why it repeats "still waiting" every 5 minutes; it does not fail.

### M5: BlackHole retention: implemented
- Design: `docs/superpowers/specs/2026-11-22-m5-blackhole-retention-design.md`; plan: `docs/superpowers/plans/2026-11-22-m5-blackhole-retention.md`.
- **Blocking already worked.** M3 files a blackholed sender's mail into the blackhole folder and M4 training makes a drag
  into it block the sender. Mail does **not** go straight to Trash; M5 adds the retention sweep.
- **Config.** `[blackhole] retention` (default `"30d"`, the usual duration units, `"off"` disables) and `trash_folder`
  (default: the folder the server flags `\Trash`; may not be INBOX or a configured folder). The section does nothing without a
  blackhole folder.
- **Sweep** (`engine.sweepBlackhole`, last step of `Process`, under the pass lock). It reads the whole blackhole folder,
  records each message's first-seen time in `blackhole_seen`, forgets messages that left, and moves every message past
  retention to Trash. A real run sweeps at most once an hour (`meta.blackhole_swept_at`); a dry run always looks and
  writes nothing. `planSweep` is pure and table-tested.
- **Age counts from arrival in the folder**, not INTERNALDATE: a move keeps INTERNALDATE, so a sender blocked on day 29
  would otherwise be trashed on day 30. The clock starts at the first sweep that sees the message (sweeps run with passes, at most hourly, so
  it is as late as the next pass after the message lands), and for mail already in the folder when M5 is installed, at the first M5 pass.
- **Never swept:** flagged messages (they stay and are counted in the report), and messages brought back with `undo`
  (found through the audit log: a done move out of the blackhole folder that is now `undone`).
- **Audit.** A purge is an ordinary `move` row (`retention: in <folder> for <retention> or more`), written `planned` before
  the move and completed with the COPYUID destination. So `undo --since`, `reconcile` and own-move recognition work
  unchanged: `undo` restores the message without training, and an interrupted purge is settled at the next pass. Dragging
  a message from Trash to INBOX is a return and pins the sender to INBOX (the way to unblock one).
- **No Trash.** If messages are due but no Trash folder is found, the pass warns (so `once` exits non-zero) and moves nothing;
  first-seen times are still saved. A failed move is a warning too and is tried again at the next sweep.
- **Report.** `ProcessReport.Purged` is separate from `Moves`, so the INBOX counts are unchanged. `once` and `run` print one
  line per message and a summary ("moved N message(s) from X to Trash (kept: 1 flagged, 2 restored by undo)").
- State: schema v6.
- Verified: unit tests (config, store migration, `planSweep` table, pass tests with the fake backend: first pass, due purge with
  audit first, dry run, hourly gate, no Trash, configured Trash, failed move, off, undo then no re-purge and no training,
  reconcile of an interrupted purge), `make itest` (a throwaway Dovecot user: the first pass only records, a dry run lists,
  the real run purges one message and keeps the flagged and the young one, `undo` restores it and it is not purged again;
  time is moved by editing the first-seen times in the database) and a manual demo.
- **Not verified:**
  - Trash special-use on real Fastmail or Gmail (Dovecot flags `\Trash` by default). On Gmail a move into `[Gmail]/Trash` is
    followed by Gmail's own deletion after 30 days.
  - a very large blackhole folder: the whole folder is read once an hour.
  - a later copy (same Message-ID) of a purged message is left in INBOX unclassified, as for any message quietbox has moved.
  - that retention counts real elapsed time: the integration test moves the stored times instead of waiting.

### M6: Open-source release
- **Docker image and compose: done.** `Dockerfile` (cross-compiling build stage, distroless static nonroot,
  base images pinned by digest, allow-list `.dockerignore`), `deploy/docker-compose.yml` (one service per account,
  non-root, read-only rootfs + `/tmp` tmpfs, all capabilities dropped, no-new-privileges, no ports, password as a
  compose secret via `password_file`; `init`'s report is written next to the database, on the volume),
  `deploy/docker-compose.build.yml`, `make docker-build` / `docker-buildx`, `.github/workflows/docker.yml`
  (ghcr.io on `v*` tags and manual runs only) and the guide `docs/DEPLOY.md` (passwords ranked, migrating an
  existing database, backups, NAS notes, troubleshooting).
  - Verified with Podman 5.2 + Docker Compose 2.29 against the Dovecot test server: arm64 image `init --yes`,
    `once`, `run` filing a delivered message, `senders set`/`undo`/`explain` next to the daemon, SIGTERM exit 0;
    amd64 cross-built (ran `--help` under emulation only); secret uid pitfall and warnings reproduced on the
    Linux VM filesystem; migration via `sqlite3 .backup` exercised on a throwaway copy.
  - Not verified: a real NAS, a real provider in the container, amd64 hardware, the publish workflow (no tag
    pushed, so nothing is on ghcr.io yet), Docker Engine (only Podman).
- **Version, license and binaries: done, not yet published.**
  - `quietbox --version` / `quietbox version`: set by `-ldflags` (`make build`, the Dockerfile, goreleaser, the image workflow);
    `go install` builds fall back to the embedded build info. The image also carries `/LICENSE` and the MIT label.
  - `.goreleaser.yaml` (pinned v2.20.3, also `make release-snapshot`): linux and darwin, amd64 and arm64, no Windows
    (the locks need `flock`). Archives hold the binary, `LICENSE`, `README.md`, the example configs, `docs/SYSTEMD.md` and the
    units; `checksums.txt`; the changelog comes from the conventional commits; the GitHub release is created as a **draft**.
    `.github/workflows/release.yml` runs it on `v*` tags, next to `docker.yml`.
  - Verified: a local snapshot build of all four targets, checksums checked, archive contents listed, the darwin/arm64 binary run on
    the host and the linux/arm64 binary run in a container, the image built twice with different version args (the compile step
    is rebuilt, so `--version` follows the args even under podman).
- **systemd: done** (`deploy/systemd/`, `docs/SYSTEMD.md`). A template unit `quietbox@.service` (instance = account, config
  `/etc/quietbox/NAME.toml`, database `/var/lib/quietbox/NAME.db`), a sysusers file and a user-service variant. A static `quietbox`
  user, not `DynamicUser`: `undo`/`senders`/`once` run beside the daemon as the same user, otherwise root-run commands would leave
  root-owned SQLite files the service cannot write. The unit waits (`ConditionPathExists`) until `init` has created the database.
  - Verified on systemd 255 (Fedora CoreOS 40, aarch64) with the linux/arm64 binary and the Dovecot test server: `systemd-analyze
    verify` on both units, exposure 1.5 (OK), no start before `init`, then a start as the `quietbox` user, a message delivered
    while it idled was filed, `undo` beside the daemon, file ownership, `systemctl stop` -> `Result=success`.
  - Not verified: other distributions, `x86_64`, a real provider, DNS under `RestrictAddressFamilies=AF_INET AF_INET6`, starting
    at boot, actually running the user unit.
- **README, examples and the SaneBox guide: done.** The README opens with what it does, safety, install, a quick start and a provider
  table; `examples/` has Fastmail, Gmail (untested) and generic IMAP; `docs/MIGRATING-FROM-SANEBOX.md`. A test parses every example
  config and every complete TOML block in the docs; the quick start was run command by command against the Dovecot server.
  Links and anchors in the docs were checked. Not verified: Gmail, Fastmail for moving mail, a real SaneBox migration.
- **Published: done** (2026-11-23). The repository is public; `v0.1.1` is the first published release (`v0.1.0` was
  deleted). CI, `docker.yml` and `release.yml` have run on GitHub, and the image was built. goreleaser creates each
  release as a **draft**: publish it with "pre-release" **unchecked**, or GitHub's `/releases/latest` redirect (which
  `install.sh` uses to find the newest version) does not resolve. (That is what happened with `v0.1.1` at first.)
- **Site and install script: done** (`site/`, `site/install.sh`, branding in `assets/`). One static page for `quietbox.sh`
  (inline CSS, no JavaScript) and `curl -fsSL https://quietbox.sh/install.sh | sh`: detects Linux/macOS and amd64/arm64,
  downloads the release archive, checks it against `checksums.txt` (refuses on a mismatch), installs to `~/.local/bin`
  (`VERSION=`, `INSTALL_DIR=`). The README carries the banner, a "brand new, tried on Fastmail and Gmail only" status
  and a use-at-your-own-risk note. `SECURITY.md` (private vulnerability reporting) and `CONTRIBUTING.md` exist.
  - Verified: the script against a local fake release (good archive installs, wrong checksum installs nothing), and the
    live script from `quietbox.sh` on macOS arm64 and in a Debian arm64 container (`quietbox --version` printed v0.1.1);
    the page at desktop and 390 px width (one overflow found and fixed); the served content types.
  - Not verified: x86_64 Linux, Intel macOS, `shellcheck`, the page in dark mode in a real browser, that private
    vulnerability reporting is switched on, that the GHCR package is public.
- **Real providers: reported working, not checked item by item.** quietbox runs against a Fastmail and a Gmail account
  (one daemon each). Still individually unconfirmed from the earlier "not verified" lists: INTERNALDATE preserved on
  MOVE (return-to-INBOX detection), Gmail advertising both `MOVE` and `UIDPLUS`, Trash special-use for blackhole
  retention (first purges are 30 days out), and M3's own "done" criterion (SaneBox off, a week of clean running).
- **Homebrew tap: set up, not yet exercised.** `michele/homebrew-quietbox` exists and `.goreleaser.yaml` has a `brews`
  section that pushes the formula at release time (token and steps in `docs/RELEASING.md`). Nothing has run it yet: the
  tap stays empty until the next tagged release, and `brew install` is not in the README or the site until then. goreleaser
  deprecates `brews` (so `goreleaser check` exits non-zero), but a formula is kept on purpose: casks are macOS only, and a
  cask download is quarantined, which the unsigned macOS binaries would trip over. Revisit when goreleaser removes it.
- **Site: status refresh, getting started guide (2026-11-24).** After a day of use on Fastmail and Gmail, the status text on the
  site and README says "new, not thoroughly tested" with no version number (so it does not need updating per release), and Gmail
  is no longer marked untested. `site/index.html` now says which providers work (and that Microsoft 365 and Outlook.com do not),
  what is not included, and how to check it before trusting it (`scan`, `eval`, `explain`). `site/guide/index.html` is a
  step-by-step guide from install to `undo`; the CSS moved to `site/style.css`. The footer credits the author. The configs in
  the guide are parsed by `TestSiteTOMLBlocksParse`, like the docs.
- **Suggested next:** the first release through the tap (then the `brew install` line), the blog post, and the open question below.

### M7: Stats: implemented
- Design: `docs/superpowers/specs/2026-11-26-stats-design.md`; plan: `docs/superpowers/plans/2026-11-26-stats.md`.
- `quietbox stats [--since DUR] [--json]` (default `30d`). Reads the database only: no IMAP login, no lock, nothing written.
  No schema change: it queries `audit` (via `AuditSince`) and `training` (`Store.TrainingSince`).
- **Filed** = `move` audit rows with status `done` in the window, except retention purges (rule `retention`), which are shown as
  "purged to Trash" on the folder they left. A move that was later undone still counts as filed.
- **Rule** = the text before the first `": "` of `audit.reason` (`other` when there is none). The reason does not say whether a
  `sender` route was learned or trained, so the breakdown is by rule, not by route source.
- **Correction** = a `training` event (`source = move`) for a message quietbox filed (not undone), dated at or after the move; one per
  message, the latest event decides "dragged back to INBOX" or "re-filed elsewhere". `senders set` events are not corrections but are
  counted in "senders retrained". `undo` is counted separately (`audit.undone`) and creates no training row.
- **Error rate** = corrections / filed. It is a floor: it only sees mistakes you noticed and fixed; `eval` measures the hidden ones.
- **Time saved** = filed x `[stats] seconds_per_message` (default 5, positive integer), always labelled an estimate.
- Code: `internal/stats` (pure `Compute`, `WriteText`, `FormatSaved`, table-tested), `cmd/quietbox/stats.go`.
- Verified: unit tests under `-race`; `make itest` (a throwaway Dovecot user: filing, a drag back, `undo`, `--json`) and a manual run.
- **Not verified:** numbers on a real mailbox; performance on a very large audit log (the window's moves are read in one query, there
  is no index on `audit.at`: measure before adding one); that 5 s per message is a fair default.

## Ideas (not planned)

- Snooze folders, reply reminders via plus addressing
- JMAP backend (Fastmail, Stalwart) for push and stable IDs
- Local LLM for low-confidence decisions
- Daily digest email of what was filed away
- More types (`receipts` split from `bulk`, `social`, `calendar`)

## Open questions

- Inbox lookback default: is 6 months right for your mailbox?

## Decided against

- Reading contacts (CardDAV, or a vCard file) as an allowlist: decided against. The owner does not want contact data
  from the mail provider pulled in; Sent mail and your own filing are the signals.
