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

A learned sender route is checked before the thread rule, so a reply from a sender you have
filed away stays filed; `quietbox eval` counts the cost of that as "hidden important".
Every decision records its **rule**, **reason** and the detector **signals** behind it
(`quietbox explain <message>` arrives in M4).

## Ongoing training

Same as SaneBox: moving a message *into* a configured folder re-routes that sender there.
User moves always override learned routes and detectors.
IMAP doesn't report moves, so on each sync quietbox compares folder contents with its
last snapshot. A message that changed folder **and that quietbox didn't move itself**
(check the audit log) counts as a user move.

## State (SQLite)

- `folders(name, type, present, last_scanned_at)` (`present`: `exists` is an SQL keyword)
- `senders(address, domain, route_folder, route_source[learned|trained|detected], mixed, sent_to_count, updated_at)`
- `observations(address, folder, internal_date)`: one row per message seen, no keys, subjects or bodies.
  Recency weighting needs per-message dates, and re-learning one folder replaces just that folder's rows.
  `sender_folder_counts` is a view over it.
- `sent_to(address, count, last_sent)`: recipients found in Sent
- `sent_message_ids(message_id, sent_at)`: Message-IDs of mail in Sent (schema v2), so a reply to
  your own mail is recognised as "in the thread". IDs only, never subjects.
- `domains(domain, default_folder, senders, agreeing)`
- `messages(key, folder, first_seen, last_seen)`: snapshot used to detect user moves
- `decisions(message_key, folder, reason, at)`
- `audit(id, action, message_key, from_folder, to_folder, reason, at, undone)`: `action` is `create_folder` in M1, moves from M3

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
    signals) and the signals that fired. Only `strong` can override a mixed sender's route.
  - `newsletter`: `List-Id` (+ `List-Unsubscribe` = strong), or a newsletter-platform domain in
    From or Return-Path (substack, beehiiv, buttondown, mailchimp, convertkit, ghost, mailerlite)
    unless the sender is a `noreply@`-style system address. An unsubscribe header alone is not
    a newsletter.
  - `bulk`: `noreply`/`notifications`/`billing`/`orders`... local part or a transactional ESP
    header (SES, Postmark, SendGrid, Mailgun); strong when a subject keyword (receipt, order,
    invoice, shipped, ricevuta, ordine, fattura, spedizione...) agrees. A keyword alone never
    fires, and mail with a `List-Id` is never bulk.
  - `later`: `Precedence: bulk|list|junk`, `Auto-Submitted`, `List-Unsubscribe`, `Feedback-ID`,
    marketing ESP headers, mass-mailer `X-Mailer`; two or more signals = strong.
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
- Message headers now also carry `In-Reply-To`, `References` and a whitelist of automation
  headers (`Signals`). IMAP `HEADER.FIELDS` has no wildcards, so ESP markers are listed by name.
- **Done when:** new-sender agreement on your real mailbox is good enough for you to trust it.
  This part cannot be verified without your mailbox: run `quietbox eval` on it.

Known limitation: `init` learns a route of INBOX for every sender who has mail in INBOX (that is
what the history says), so `scan` on a mailbox that `init` has just read will mostly show
"sender route to INBOX". Detectors matter for senders that appear after `init`. Whether an
INBOX-only history should count as a learned route is something to decide from `eval` numbers.

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

- Should `init` read Fastmail contacts (CardDAV) as an allowlist, or just a vCard file?
- Inbox lookback default: is 6 months right for your mailbox?
