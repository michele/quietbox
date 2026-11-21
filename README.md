# quietbox

A self-hosted, open-source alternative to SaneBox: a Go CLI/daemon that watches an IMAP
mailbox and moves non-urgent mail out of the inbox, learning from the folders you move mail into.

**Status: M4 (training).** `check`, `init`, `senders` (with `set`/`unset`), `scan`, `eval`, `once`,
`run`, `undo` and `explain` exist. quietbox learns where your mail goes today (`init`), then files
**new** INBOX mail as it arrives (`once` for cron, `run` as a daemon), and learns from the mail you
move by hand: file a message into a folder and that sender goes there; drag one back to INBOX and
that sender stays in INBOX. It never deletes or expunges anything, every move is written to an audit
log first, and `undo` reverses it. **It has not been tried on a real provider yet**: it has been run
end to end only against the Dovecot test server. See [docs/PLAN.md](docs/PLAN.md) for the roadmap.

## Configuration

One TOML file (default `$XDG_CONFIG_HOME/quietbox/config.toml`, else `~/.config/quietbox/config.toml`).
The IMAP password is **never** written in the config file (a `password` key is an error). It comes from
one of three places; see [Password sources](#password-sources). The simplest is the environment:

```sh
export QUIETBOX_IMAP_PASSWORD='app-password-here'
```

```toml
[imap]
host = "imap.fastmail.com"
username = "you@example.com"
# port defaults to 993 (tls = "implicit") or 143 (starttls / none)
# tls = "implicit" | "starttls" | "none"   ("none" only allowed for loopback hosts)

[[folder]]
name = "INBOX"          # exactly one important folder, and it must be INBOX
type = "important"

[[folder]]
name = "SaneLater"      # at least one "later" folder is required
type = "later"

[[folder]]
name = "SaneNews"
type = "newsletter"

[[folder]]
name = "SaneBulk"
type = "bulk"

[[folder]]
name = "SaneBlackHole"  # optional, at most one
type = "blackhole"
```

Folder types: `important`, `newsletter`, `bulk`, `later`, `blackhole`.

If a type has several folders, automatic detection only files mail into one of them if
exactly one sets `detect = true`; otherwise detection is off for that type and `check` warns.
Unknown keys (including a `password` key) are errors.

Optional sections (defaults shown):

```toml
[me]
addresses = ["you@example.com"]   # your own addresses; default: imap.username

[learn]
inbox_lookback = "6mo"            # how far back INBOX is read (units: h, d, w, mo, y)
sent_folder = ""                  # default: the folder the server marks \Sent
window = 20                       # newest N messages per sender
half_life = "90d"                 # recent mail counts more
mixed_share = 0.2
sent_override_share = 0.75
domain_min_senders = 3
domain_share = 0.8
personal_domains = []             # extra domains that never get a domain default

[run]
skip_seen = true                  # leave mail you have already read where it is
skip_flagged = true               # leave flagged mail where it is

[state]
path = ""                         # default: $XDG_STATE_HOME/quietbox/quietbox.db
```

## Usage

```sh
make build
QUIETBOX_IMAP_PASSWORD=... ./bin/quietbox check --config path/to/config.toml
```

`check` connects, prints the server capabilities, says whether stable `EMAILID` message keys
are available (otherwise it falls back to the `Message-ID` header), and shows which configured
folders exist, their message counts and which folder is the detection target for each type.
It is read-only.

### Learning: `init` (alias `learn`)

```sh
quietbox init --dry-run     # see what it would do; changes nothing, still writes learn-report.csv
quietbox init               # create missing folders (asks first), learn, save
quietbox senders --mixed    # review what was learned (database only, no IMAP login)
```

`init` reads message **headers only**, in read-only mode: nothing is marked as read and no
message body is downloaded. It scans the configured folders, Sent (the strongest "important"
signal) and the last `inbox_lookback` of INBOX, works out a route for every sender, and writes
`learn-report.csv` so you can review the result before any mail is ever moved. Re-running it is
safe and rebuilds the learned data.

| Flag | Meaning |
|---|---|
| `--dry-run` | no folders created, no database written |
| `--yes`, `-y` | create missing folders without asking (without it, a non-interactive run skips creation) |
| `--since 2y` | limit scan depth (INBOX is also capped by `inbox_lookback`) |
| `--folder SaneNews` | re-learn one folder only (needs a previous full run) |
| `--report path` | where to write the CSV (default `learn-report.csv`, mode 0600; empty to skip) |

The report lists every sender with its route, whether it is **mixed** (it legitimately appears in
more than one folder), how many messages it has in each folder, and why.

### Shadow mode: `scan`

```sh
quietbox scan                    # what would happen to INBOX mail, and why
quietbox scan --since 30d --all --csv scan.csv
```

`scan` classifies the mail in INBOX with what `init` learned and prints where each message would
go (a learned sender route, a domain default, a reply in a thread you took part in, or a
newsletter / bulk / later detector) with the reason. It only reads: it needs a previous `init`,
writes nothing to the mailbox or the database, and its reports have sender addresses but never
subjects or bodies. By default it lists the messages that would leave INBOX (newest 50); the
totals always cover everything scanned.

### How good would it be? `eval`

```sh
quietbox eval                          # learn from mail older than 60 days, test on the rest
quietbox eval --cutoff 2026-03-01 --csv disagreements.csv
```

`eval` learns only from mail that arrived before the cutoff, then classifies the mail after it and
compares with the folder that mail is actually in (where SaneBox or you put it). Senders it already
knew agree almost by construction, so read the **new senders** line: it measures the detectors.
**Hidden important** counts INBOX mail quietbox would have filed away, the failure to watch. The
report also splits all disagreements into hidden / leaked / cross-filed, says which rule caused each
hidden message, and shows agreement per rule and detector strength plus the most common signal
combinations behind new-sender decisions. Those tables are aggregate (no addresses, subjects or
header text), so they are safe to paste when asking for help tuning. `eval` reads the mailbox and
writes nothing (no database either).

To measure a change before adopting it, `eval` can switch on an experiment; the report names it
and you compare runs with the same `--cutoff`. There is one today:

| Flag | What it tries |
|---|---|
| `--mixed-override` | a strong detector match overrides a mixed sender's majority route (off by default: right 1 time in 4 on a real mailbox) |

The report also has a "Thread check" line (how many of your sent Message-IDs were learned and how
many test messages reply to one), so you can see that thread matching works on your mail.

### Moving mail: `once`, `run`, `undo`

```sh
quietbox once --dry-run     # what would be filed right now; changes nothing
quietbox once               # file what arrived since the last run, then exit (cron)
quietbox run                # stay running: IDLE on INBOX, file mail as it arrives
quietbox undo --since 1h    # move back what quietbox filed in the last hour
```

Only mail that arrives **after** `quietbox init` is ever touched: `init` records where each folder
ends (UIDNEXT), and every run looks only at newer mail, so your existing inbox is never swept.
`once` and `run` also read new mail in Sent, so people you have just written to stay in INBOX.

Safety:

- **Needs `MOVE` and `UIDPLUS`.** quietbox refuses to run on a server without both (`check` shows
  the capabilities). It never uses COPY + delete + expunge, so nothing is ever expunged or deleted.
- **Audit first.** Every move is recorded (message key, from, to, reason, time) *before* it runs
  and completed afterwards. A run killed half-way is settled on the next start.
- **Left alone:** mail you have already read (`\Seen`) or flagged (`skip_seen`, `skip_flagged`),
  and any message quietbox has moved before. Drag a message back to INBOX, or `undo` it, and it
  stays there.
- **UIDVALIDITY.** If the server renumbers INBOX or Sent, nothing is moved: quietbox says so and
  asks you to re-run `init`.
- **One at a time.** A lock file next to the database stops two instances acting on one mailbox
  (`--dry-run` does not need it). Unix only. `undo` also takes the lock, so stop `quietbox run`
  before undoing (or use `undo --dry-run` while it runs).
- `once` exits non-zero if a move failed or it had a warning, so cron mails you.
- A failed move stays in INBOX and is not retried. `run` retries a lost connection with a growing
  delay (5s up to 5 min) and stops on anything that needs you (no MOVE, nothing learned yet).
- `undo` leaves alone anything you moved yourself since, and reports it. The audit log stores no
  addresses or subjects, so `undo` shows folders and the reason a message was filed.
- Sender routes change when you re-run `init`, when you move mail by hand (see below) and with
  `senders set`/`unset`.

### Training: move mail by hand

Teach quietbox the way you taught SaneBox, from any mail client:

- **Move a message into a configured folder** (from INBOX or from another folder): that sender's mail
  goes there from now on.
- **Move a message back to INBOX** from a configured folder: that sender stays in INBOX from now on.
- One move is enough and the **last move wins**. Your moves beat everything quietbox learned or detected.

```sh
quietbox once            # ... trained writer@news.test: you moved a message from SaneNews to INBOX
quietbox senders         # the sender now shows ROUTE INBOX, SOURCE trained
```

How it works: every configured folder has a cursor like INBOX, and each `once`/`run` pass reads only
the mail that **arrived** in it since the last pass, *before* it files new INBOX mail (so the mail that
woke `run` already goes where you just said). An arrival that quietbox did not put there itself is a
move by you; quietbox recognises its own moves by the UID they landed on, so its own filing and `undo`
never train. A message arriving in INBOX counts as "moved back" only if it was last seen in a
configured folder and keeps its original arrival date (a move does, a fresh delivery does not).
Training changes only the local database, never the mailbox.

- `run` waits (IDLE) on INBOX only. A move into another folder is noticed at the next pass, which the
  next INBOX delivery triggers.
- A second copy of a message quietbox already filed (same Message-ID, delivered again) stays in INBOX
  untouched and teaches nothing.
- Mail from your own addresses, and mail without a usable From, never trains.
- `once --dry-run` prints `would train ...` and writes nothing.
- **Upgrading a database from before M4:** training starts at the first `once`/`run` (a `note:` says
  so); moves made earlier are not learned, and messages you filed by hand earlier are only recognised
  when you move them back to INBOX after you re-run `init` (or `learn`).
- There is no setting to turn training off; `senders unset` undoes a trained route.

### Setting routes by hand: `senders set`, `senders unset`

```sh
quietbox senders set promo@deals.test SaneBulk   # always file this sender into SaneBulk
quietbox senders set alice@friends.test INBOX    # always keep this sender in INBOX
quietbox senders unset promo@deals.test          # remove the trained route
```

- The folder must be one of your configured folders (or INBOX). Addresses are matched lower-case.
- `unset` removes only a trained route. If the sender had a learned route before you trained it, that
  route comes back at the next `init`/`learn`, not immediately: until then the sender has no route.
- Both work on the database only (no IMAP login) and take the instance lock, **waiting** for it
  ("waiting for another quietbox...") rather than failing. `quietbox run` holds the lock for as long as
  it runs, so **while a daemon is up they wait until it stops**: stop `run`, set the route, start it
  again (Ctrl-C stops the waiting).

### Why did it do that? `explain`

```sh
quietbox explain '<message-id@example.com>'     # look up by Message-ID (angle brackets optional)
quietbox explain --folder SaneNews 1234         # or by UID in a folder (default INBOX)
```

`explain` shows what quietbox would decide for that message **now**: the target folder, the rule,
the reason and detector signals, the sender's route and where it came from (learned or trained), the
latest training event, and the audit log rows for that message. It opens the folder read-only, takes
no lock and writes nothing. It prints the sender address, UID and Message-ID, never the subject or body.

### Password sources

Environment variables are visible in `/proc/<pid>/environ`, `systemctl show`, child processes and crash
dumps, so for anything long-running prefer a file or a command. In `[imap]`, set **at most one** of:

```toml
[imap]
password_file = "/run/secrets/imap-password"     # a file holding just the password
# password_file = "${CREDENTIALS_DIRECTORY}/imap" # systemd: LoadCredential=imap:/etc/quietbox/imap.pw
# password_file = "~/.config/quietbox/gmail.pw"   # ~/ and $VAR are expanded; relative = next to this config
# password_command = "security find-generic-password -s quietbox-gmail -w"   # macOS Keychain
# password_command = "pass show mail/gmail"                                   # pass, 1Password CLI, Vault, ...
```

- Either one wins over the environment variables. With neither, `QUIETBOX_IMAP_PASSWORD_<NAME>` /
  `QUIETBOX_IMAP_PASSWORD` work as before.
- **`password_file`** must be one non-empty line. A file readable by group or others is **refused**
  (`chmod 600` it), except inside a container (Docker, Podman, Kubernetes), where mounted secrets are
  usually world-readable: there it only **warns**, on every command. Container detection looks at
  `/.dockerenv`, `/run/.containerenv` and `KUBERNETES_SERVICE_HOST`; if it guesses wrong, set
  `QUIETBOX_CONTAINER=1` (it is a container) or `QUIETBOX_CONTAINER=0` (or `false`, `no`, `off`: it is not). Symlinks are followed.
- **`password_command`** runs through `sh -c` with a 30 second limit; its output must be one non-empty line.
  If it fails you see its exit status and stderr, never its output. The config file is trusted input: do not
  let others write to it.
- A systemd unit can use `LoadCredential=` (or `LoadCredentialEncrypted=` with `systemd-creds encrypt`, so the
  password is encrypted at rest) together with `password_file = "${CREDENTIALS_DIRECTORY}/imap"`.
- `quietbox run` re-reads the file or re-runs the command on every reconnect, so a rotated password needs no
  restart. A failing command or an unreadable file is retried with the usual backoff.
- Commands that never connect (`senders`, `accounts`) do not touch the password source.
- Nothing ever prints or logs the password.

### Several accounts

Run **one quietbox process per account**, each with its own config file, database and lock file.
Put each account's config in the config folder (`~/.config/quietbox/`, or `$XDG_CONFIG_HOME/quietbox/`)
as `NAME.toml` and select it with `--account`:

```sh
quietbox accounts                      # list the accounts in the config folder
export QUIETBOX_IMAP_PASSWORD_GMAIL=...        # QUIETBOX_IMAP_PASSWORD_<NAME>, '-' becomes '_'
quietbox --account gmail init --dry-run
quietbox --account gmail once
quietbox --account fastmail run
```

- `--account NAME` and `--config PATH` are alternatives (giving both is an error). Names use `a-z`, `0-9`, `-`, `_`.
- With `--account`, the password comes from `QUIETBOX_IMAP_PASSWORD_<NAME>`, falling back to
  `QUIETBOX_IMAP_PASSWORD`, and the default database is `<state dir>/NAME.db` (so accounts never share
  one by default). With `--config` nothing changes: `quietbox.db` and `QUIETBOX_IMAP_PASSWORD`.
- A database remembers which mailbox (`username@host`) it belongs to and refuses to be used for another
  one, with a message telling you to set `[state] path`. Existing databases adopt the account that first
  opens them.
- For several accounts running as services, use one service per account (for example `quietbox --account gmail run`
  in its own systemd unit or container). Service files and a Docker image come with M6.

### Starting on a never-managed account

An account no tool has sorted before has nothing to learn from: everything sits in INBOX because nothing
moved it, so its INBOX history says nothing about where mail belongs. Use `--fresh`:

```sh
quietbox --account gmail init --fresh --backlog 30d --dry-run   # how much would be considered?
quietbox --account gmail init --fresh --backlog 30d             # learn (moves nothing)
quietbox --account gmail once --dry-run                         # review every decision
quietbox --account gmail once                                   # sort it; `undo --since 1h` reverses
```

- `--fresh` does not learn INBOX history as sender routes. Sent (people you have written to stay in
  INBOX) and the typed folders still count, and so do the detectors.
- `--backlog 30d` (needs `--fresh`) lets the first `once` also consider the INBOX mail of the last 30
  days. Backlog mail is mostly already read, so it is swept even though `skip_seen` is on; **flagged mail
  is still left alone**, and so is anything quietbox has moved before. Without `--backlog`, only mail
  arriving after `init` is touched, as always.
- With no history the detectors decide alone, so start with a short window and read the dry run
  carefully. Moving mistakes back by hand trains quietbox (see [Training](#training-move-mail-by-hand)).

## Development

```sh
make test     # go test ./... -race
make lint     # go vet + golangci-lint (fetched via `go run`, pinned version)
make build    # static binary in ./bin (CGO_ENABLED=0)
make itest    # starts the throwaway Dovecot server, seeds it, runs integration tests, tears down
```

`make itest` uses `docker compose`, or `podman compose` when there is no `docker`
(override with `COMPOSE=...`). The test server is Dovecot 2.4.5, pinned by digest, bound to
`127.0.0.1:1143`, with mail on tmpfs. Seed mail lives in `testdata/seed/`. Tests never touch a
real mailbox.

Note: Dovecot 2.4.5 does not advertise `OBJECTID`, so the local tests only exercise the
`Message-ID` key fallback. The `EMAILID` path needs a server that supports RFC 8474 (e.g. Fastmail).
