# quietbox: plan

## Goal

Replace SaneBox for a personal mailbox with a self-hosted tool that works with **any IMAP
provider** (developed against Fastmail). It runs as a long-lived process on a NAS or VPS,
or as a one-shot command from cron.

## Non-goals (for now)

- LLM classification (planned later, behind the same `classify` interface)
- Web UI. All control happens through mail folders plus the CLI.
- Multiple accounts per process (run one instance per account)
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
  `detect = true` is not allowed on `blackhole`. The IMAP password comes only from
  `QUIETBOX_IMAP_PASSWORD`.
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
`later`). For a mixed sender, the sender route is the default, and the per-message
detector may override it when it matches strongly (e.g. "Your order has shipped" → `bulk`).

## Classification order (first match wins)

1. sender is blocked → `blackhole`
2. sender has a learned/trained route → that folder (per-message override for mixed senders)
3. you have sent mail to the sender, or you're in the thread → `important`
4. domain default → that folder
5. type detectors (`newsletter`, then `bulk`, then `later`)
6. no match → stays in INBOX

Every decision records its **reason** (`quietbox explain <message>`).

## Ongoing training

Same as SaneBox: moving a message *into* a configured folder re-routes that sender there.
User moves always override learned routes and detectors.
IMAP doesn't report moves, so on each sync quietbox compares folder contents with its
last snapshot. A message that changed folder **and that quietbox didn't move itself**
(check the audit log) counts as a user move.

## State (SQLite)

- `folders(name, type, exists, last_scanned_at)`
- `senders(address, domain, route_folder, route_source[learned|trained|detected], mixed, sent_to_count, updated_at)`
- `sender_folder_counts(address, folder, count, last_seen)`: raw learning data
- `domains(domain, default_folder, confidence)`
- `messages(key, folder, first_seen, last_seen)`: snapshot used to detect user moves
- `decisions(message_key, folder, reason, at)`
- `audit(id, message_key, from_folder, to_folder, reason, at, undone)`

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
- folder creation (the only mutation before M3), header scanning, Sent + INBOX scan
- sender routes, mixed senders, domain defaults
- `quietbox senders [--folder X] [--mixed]`, `learn-report.csv`
- **Done when:** on your real mailbox the report matches your intuition and the
  runtime is acceptable (measure it; batch FETCH of headers only).

### M2: Detectors + shadow mode (still read-only)
- `newsletter`, `bulk`, `later` detectors, table-tested with real header samples
- `quietbox scan --report`: what quietbox would do with INBOX mail, and why
- **hold-out evaluation:** learn only from mail older than a cutoff date, then classify
  mail received after it and compare with where SaneBox actually filed it. Known senders
  will agree almost by construction, so report agreement **separately for new senders**:
  that number measures the detectors.
- **Done when:** new-sender agreement is good enough for you to trust it.

### M3: Run for real
- `quietbox run` (daemon, IDLE on INBOX) and `quietbox once` (cron)
- `--dry-run`, audit log, `quietbox undo --since 1h`
- **Done when:** SaneBox is switched off and quietbox has run for a week.

### M4: Training via folder moves
- snapshot diff, user-move detection, `quietbox explain`, `quietbox senders set <addr> <folder>`

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

- Default for mixed senders when the detectors don't fire: majority folder or INBOX?
- Should `init` read Fastmail contacts (CardDAV) as an allowlist, or just a vCard file?
- Inbox lookback default: is 6 months right for your mailbox?
