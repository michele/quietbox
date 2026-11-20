# quietbox

A self-hosted, open-source alternative to SaneBox: a Go CLI/daemon that watches an IMAP
mailbox and moves non-urgent mail out of the inbox, learning from the folders you move mail into.

**Status: M2 (shadow mode).** `check`, `init`, `senders`, `scan` and `eval` exist. quietbox learns
where your mail goes today and can tell you what it *would* do with new mail, but it does not move
any mail yet; the only change it can make to your mailbox is creating configured folders that are
missing. See [docs/PLAN.md](docs/PLAN.md) for the roadmap.

## Configuration

One TOML file (default `$XDG_CONFIG_HOME/quietbox/config.toml`, else `~/.config/quietbox/config.toml`).
The IMAP password is **never** read from the file. Set it in the environment:

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
