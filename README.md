<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/banner-dark.svg">
    <img alt="quietbox: a calmer inbox, on a server you own" src="assets/banner-light.svg" width="640">
  </picture>
</p>

A self-hosted, open-source alternative to SaneBox. quietbox watches an IMAP mailbox and moves
non-urgent mail (newsletters, receipts, notifications, blocked senders) out of your inbox into
folders you choose, **learning from how you file mail yourself**. One static Go binary, run from
cron or as a daemon, for any IMAP server that supports `MOVE` (developed against Fastmail).

> **Status: new, and not thoroughly tested.** It is in daily use on Fastmail and Gmail accounts and has not
> been tried on other providers; it should work with any IMAP server that supports `MOVE` and `UIDPLUS`, but other providers are
> unverified. Start with the dry runs, keep `undo` in mind, and expect rough edges. What is and is not
> verified is listed per milestone in [docs/PLAN.md](docs/PLAN.md).
>
> **Use it at your own risk.** quietbox moves mail and never deletes it, and `undo` reverses its moves, but it is
> released under the [MIT licence](LICENSE) with no warranty. Keep a backup of any mailbox you cannot afford to
> lose mail from.

- New here? The [getting started guide](https://quietbox.sh/guide/) walks through it step by step.
- [What it does](#what-it-does) · [Safety](#safety) · [Install](#install) · [Quick start](#quick-start) · [Providers](#providers)
- [Coming from SaneBox](docs/MIGRATING-FROM-SANEBOX.md) · [Running it: systemd](docs/SYSTEMD.md), [Docker](#running-in-docker)
- Reference: [Configuration](#configuration) · [Usage](#usage) · [Locks](#locks) · [Training](#training-move-mail-by-hand) · [Passwords](#password-sources) · [Several accounts](#several-accounts)

## What it does

You give quietbox a few folders, each with a **type**: `important` (your INBOX), `later`, `newsletter`,
`bulk` (receipts, notifications), and optionally `blackhole` (senders you never want to see).

1. `quietbox init` reads your existing folders and Sent mail and learns where each sender's mail belongs.
2. `quietbox once` (cron) or `quietbox run` (daemon) files **new** mail as it arrives, by what it learned and by
   detectors for newsletters, bulk mail and automated mail. Mail it cannot place stays in INBOX: hiding a real
   person's email is the failure it tries hardest to avoid.
3. When you move a message into a folder (or back to INBOX), that sender follows. One move is enough.
4. Mail in the blackhole folder moves to Trash after 30 days (configurable), never sooner, and never deleted by quietbox.

Not included: a web interface, snooze and reply reminders, digests, OAuth2 (see [Providers](#providers)),
Windows (the instance locks need Unix `flock`). One quietbox process runs per mailbox account.

## Safety

- **Never deletes or expunges.** Everything is a `MOVE`; "delete" means Trash. Without `MOVE` and `UIDPLUS` on the
  server, quietbox refuses to run.
- **Audit first.** Every move is written to a local audit log before it happens, and `quietbox undo --since 1h`
  reverses it. Every command that changes anything has `--dry-run`.
- **Headers only.** Message bodies are never downloaded. Passwords never go in the config file (`password_file`,
  `password_command` or an environment variable) and are never logged.
- **New mail only.** `init` marks where each folder ends; `once` and `run` never touch mail that was already there.

## Install

- **Install script** (Linux and macOS, amd64 and arm64): `curl -fsSL https://quietbox.sh/install.sh | sh`
  installs the latest release into `~/.local/bin` after checking it against `checksums.txt`
  ([read it first](site/install.sh); `VERSION=` and `INSTALL_DIR=` override the defaults).
- **Binary** (same platforms): download the archive from the
  [releases page](https://github.com/michele/quietbox/releases), check it against `checksums.txt`
  (`shasum -a 256 -c checksums.txt --ignore-missing`), unpack it and put `quietbox` on your `PATH`. The macOS
  binaries are not signed or notarized, so Gatekeeper will refuse the first run: `xattr -d com.apple.quarantine quietbox`,
  or build from source.
- **With Go** (1.26 or newer): `go install github.com/michele/quietbox/cmd/quietbox@latest`.
- **From source:** `make build` puts a static binary in `./bin`.
- **Always on:** [systemd units](docs/SYSTEMD.md), or the [container image](#running-in-docker) (`ghcr.io/michele/quietbox`).

`quietbox --version` prints the version.

## Quick start

```sh
export QUIETBOX_IMAP_PASSWORD='your-app-password'          # see Providers for where to get one
mkdir -p ~/.config/quietbox
cp examples/fastmail.toml ~/.config/quietbox/config.toml   # edit the host and username

quietbox check              # connects: capabilities, and which folders exist
quietbox init --dry-run     # read-only: what it would learn (writes only the report)
quietbox init               # creates missing folders (asks first; --yes skips the question) and learns
quietbox scan               # read-only: what it would do with your INBOX right now
quietbox once --dry-run     # what the next pass would move; changes nothing
quietbox once               # file new mail (run it from cron), or: quietbox run
quietbox undo --since 1h    # change your mind
```

Review the CSV report `init` writes before anything is filed. `quietbox eval` measures how well it would
have done on mail you have already sorted. If your mailbox has never been sorted by anything, read
[Starting on a never-managed account](#starting-on-a-never-managed-account) first.

## Providers

| Provider | Status | Config |
|---|---|---|
| Fastmail | what it was developed against; app password | [`examples/fastmail.toml`](examples/fastmail.toml) |
| Gmail | in daily use, not thoroughly tested; IMAP with an [app password](https://myaccount.google.com/apppasswords) (needs 2-Step Verification) | [`examples/gmail.toml`](examples/gmail.toml) |
| Any IMAP server with `MOVE` and `UIDPLUS` | tested against Dovecot 2.4.5 only; `quietbox check` shows what your server offers | [`examples/generic-imap.toml`](examples/generic-imap.toml) |
| Microsoft 365, Outlook.com | **not supported**: IMAP needs OAuth2, which quietbox does not implement | |

An **app password** is a separate password your provider creates for one application, so you never give
quietbox your real one. Create it in your provider's security settings (Fastmail: Settings, Privacy & Security,
Integrations, New app password, "Mail (IMAP)"), and keep it out of the config file: see
[Password sources](#password-sources).

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
name = "+QuietLater"      # at least one "later" folder is required
type = "later"

[[folder]]
name = "+QuietNews"
type = "newsletter"

[[folder]]
name = "+QuietBulk"
type = "bulk"

[[folder]]
name = "+QuietBlackHole"  # optional, at most one
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

[blackhole]
retention = "30d"                 # move mail out of the blackhole folder to Trash after this long; "off" to keep it
trash_folder = ""                 # default: the folder the server marks \Trash

[stats]
seconds_per_message = 5           # `quietbox stats` estimates time saved as filed messages x this

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
quietbox init --dry-run     # see what it would do; changes nothing, still writes the report
quietbox init               # create missing folders (asks first), learn, save
quietbox senders --mixed    # review what was learned (database only, no IMAP login)
```

`init` reads message **headers only**, in read-only mode: nothing is marked as read and no
message body is downloaded. It scans the configured folders, Sent (the strongest "important"
signal) and the last `inbox_lookback` of INBOX, works out a route for every sender, and writes
a CSV report next to the database (whatever `[state] path` is), named after it: e.g.
`~/.local/state/quietbox/quietbox.learn-report.csv`, or `auralis.learn-report.csv` with
`--account auralis` when `[state] path` is unset. Review it before any mail is ever moved.
Re-running `init` is safe, rebuilds the learned data and replaces the report. An old
`learn-report.csv` in the working directory is no longer written (nor deleted).

| Flag | Meaning |
|---|---|
| `--dry-run` | no folders created, no database written (the report is, creating the state directory if needed) |
| `--yes`, `-y` | create missing folders without asking (without it, a non-interactive run skips creation) |
| `--since 2y` | limit scan depth (INBOX is also capped by `inbox_lookback`) |
| `--folder +QuietNews` | re-learn one folder only (needs a previous full run) |
| `--report path` | where to write the CSV instead (default `<database name>.learn-report.csv` next to the database; mode 0600, replaced atomically; `--report ''` to skip). If it cannot be written, `init` prints a warning and still succeeds: what it learned is already saved |
| `--lock-timeout 2m` | how long to wait for another quietbox on this mailbox (see "Locks" below) |

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
- **One at a time, with `run` up.** Two lock files next to the database keep quietbox processes from
  acting on one mailbox at the same time (see "Locks" below). `run` holds its own lock for as long as
  it runs, so a second `run` on the same mailbox is refused ("another quietbox run is already
  running"), but it holds the shared one only while a pass is executing. While it waits for mail,
  `once`, `undo`, `init`, `senders set` and `senders unset` all work; if a pass is in progress they
  wait for it to end. `--dry-run` takes no lock and never waits. Unix only.
- `once` exits non-zero if a move failed or it had a warning, so cron mails you.
- A failed move stays in INBOX and is not retried. `run` retries a lost connection with a growing
  delay (5s up to 5 min) and stops on anything that needs you (no MOVE, nothing learned yet).
- `undo` leaves alone anything you moved yourself since, and reports it. The audit log stores no
  addresses or subjects, so `undo` shows folders and the reason a message was filed.
- Sender routes change when you re-run `init`, when you move mail by hand (see below) and with
  `senders set`/`unset`.

#### Locks

- **Two lock files**, both next to the database: `<state path>.lock.run`, which only `run` takes, at
  start, failing at once if another `run` has it; and `<state path>.lock`, the pass lock, which `run`
  holds only while a pass executes (never while it waits for mail or for a reconnect) and which
  `once`, `undo`, a real `init`/`learn`, `senders set` and `senders unset` take for their work.
- **Waiting.** `once`, `undo` and `init`/`learn` wait for the pass lock at most `--lock-timeout`
  (default `2m`; a Go duration such as `90s`, `2m`, `1m30s`, or `1h`, `1d`), saying "waiting for another
  quietbox..." after a second, then fail with "another quietbox is still running on this mailbox after
  waiting 2m0s". The bound is for cron: a hung process does not pile up waiting jobs. `senders
  set`/`unset` wait without a limit (Ctrl-C stops them); `run` waits for the pass lock before each pass,
  also without a limit (Ctrl-C or SIGTERM stops it while it waits). It says "waiting for another
  quietbox... (lock file ...)" after a second and then "still waiting for the pass lock after 15m (lock
  file ...)" every 5 minutes, so a stuck daemon is visible in its log.
- **A suspended or hung process still holds its lock.** A `once` stopped with Ctrl-Z (SIGSTOP), or an
  `init` sitting at its question, is alive, so its lock is held and the others wait: that is what
  `--lock-timeout` is for, and why `run` keeps saying "still waiting". Find the holder with
  `lsof <state path>.lock` (or `lsof <state path>.lock.run` for a refused `run`), then resume, finish
  or stop it.
- **Never delete the lock files while quietbox is running.** A lock is held on the open file, not on
  the name: a deleted and re-created `.lock.run` is a new, free lock, so a second `run` could start
  beside the first (and the same for the pass lock). Leaving them on disk costs nothing.
- **A dying process never leaves a lock behind.** The locks are `flock(2)` locks, which the kernel drops
  when the process exits, however it exits (a crash, `kill -9`, the machine losing power). There are no
  PID files and nothing to clean up: the empty `.lock` and `.lock.run` files stay on disk and are
  harmless, because only a lock held by a live process means anything.
- **A real `init`/`learn` holds the pass lock for the whole call**, including while it asks whether to
  create missing folders and during a long full scan. Meanwhile a running daemon's passes and
  `senders set` wait, and a cron `once` can time out. For a big `init` with `run` up, use `--yes` (no
  question to wait on) or stop `run` first.
- Keep `[state] path` on a **local filesystem**: `flock` on a network filesystem (NFS, SMB) can be
  unreliable, and quietbox does not try to detect that.
- **Restart `run` after editing the config.** The daemon keeps the config it started with. `init` and
  `senders set` work while it is up (their changes to the database are read by the next pass), but a
  folder you add to the config is not trained from (mail you move there is not learned) until `run`
  restarts.

### Training: move mail by hand

Teach quietbox the way you taught SaneBox, from any mail client:

- **Move a message into a configured folder** (from INBOX or from another folder): that sender's mail
  goes there from now on.
- **Move a message back to INBOX** from a configured folder: that sender stays in INBOX from now on.
- One move is enough and the **last move wins**. Your moves beat everything quietbox learned or detected.

```sh
quietbox once            # ... trained writer@news.test: you moved a message from +QuietNews to INBOX
quietbox senders         # the sender now shows ROUTE INBOX, SOURCE trained
```

How it works: every configured folder has a cursor like INBOX, and each `once`/`run` pass reads only
the mail that **arrived** in it since the last pass, *before* it files new INBOX mail (so the mail that
woke `run` already goes where you just said). An arrival that quietbox did not put there itself is a
move by you; quietbox recognises its own moves by the UID they landed on, so its own filing and `undo`
never train. A message arriving in INBOX counts as "moved back" only if it was last seen in a
configured folder and keeps its original arrival date (a move does, a fresh delivery does not).
Training changes only the local database, never the mailbox.

- `run` waits (IDLE) on INBOX only. A move into another folder is noticed at the next pass, which
  comes with the next INBOX delivery, a drag back to INBOX (the new message wakes `run` too), or at
  the latest 20 minutes later (the IDLE refresh).
- A later copy (same Message-ID, delivered again) of any message quietbox has seen in a configured
  folder, whether it filed the message or `init` saw it there, stays in INBOX untouched (not
  classified) and teaches nothing.
- On a server that does not keep the arrival date (INTERNALDATE) when a message is moved, a drag
  back to INBOX is not learned; the message is never moved out of INBOX again either. Dovecot keeps
  it; other servers are not verified.
- Mail from your own addresses, and mail without a usable From, never trains.
- `once --dry-run` prints `would train ...` and writes nothing.
- **Upgrading a database from before M4:** training starts at the first `once`/`run` (a `note:` says
  so); moves made earlier are not learned, and messages you filed by hand earlier are only recognised
  when you move them back to INBOX after you re-run `init` (or `learn`).
- There is no setting to turn training off; `senders unset` undoes a trained route.

### Blocking senders: the blackhole folder

Move a message into the blackhole folder and that sender is blocked: their future mail goes to the
same folder instead of INBOX (see "Training" above). Nothing is deleted at that point, so you can look
through it and rescue anything.

Mail that has been in the folder longer than `[blackhole] retention` (default `30d`) is then moved to
**Trash**. quietbox only moves it, it never expunges or deletes: emptying Trash is up to you and your
provider (Gmail, for one, deletes Trash after 30 days).

- **The 30 days start when the message lands in the blackhole folder**, not when it was sent. quietbox
  notices a message at the first sweep that sees it (sweeps run with `once` and `run` passes, at most
  hourly), so with `once` in cron the clock starts at the next run after it arrives.
- **Flagged mail is never moved out.** Unflag it and it goes at the next sweep.
- The sweep runs at the end of `once` and `run`, at most once an hour. `once --dry-run` shows what
  would go to Trash. Each move is audited first and listed in the report.
- **`undo --since` brings a message back**, and quietbox will not move it to Trash again. To unblock a
  sender, drag one of their messages (from the blackhole folder or from Trash) back to INBOX.
- Trash is the folder the server flags `\Trash`; set `trash_folder` if yours has no flag. Without a
  Trash folder, `once` warns and leaves the mail where it is.
- `retention = "off"` turns the sweep off and the folder keeps everything.

### Setting routes by hand: `senders set`, `senders unset`

```sh
quietbox senders set promo@deals.test +QuietBulk   # always file this sender into +QuietBulk
quietbox senders set alice@friends.test INBOX    # always keep this sender in INBOX
quietbox senders unset promo@deals.test          # remove the trained route
```

- The folder must be one of your configured folders (or INBOX), and the address must contain `@`;
  both are checked before waiting for the lock, so a typo fails at once. Addresses are matched lower-case.
- `unset` removes only a trained route. If the sender had a learned route before you trained it, that
  route comes back at the next `init`/`learn`, not immediately: until then the sender has no route.
- Both work on the database only (no IMAP login) and take the pass lock, **waiting** for it
  ("waiting for another quietbox...") rather than failing (Ctrl-C stops the waiting). With `quietbox run`
  up they wait at most for the pass in progress: `run` holds the lock only while a pass executes, so
  while it idles they complete at once, and the next pass of the same daemon already uses the new route.

### Why did it do that? `explain`

```sh
quietbox explain '<message-id@example.com>'     # look up by Message-ID (angle brackets optional)
quietbox explain --folder +QuietNews 1234         # or by UID in a folder (default INBOX)
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

Run **one quietbox process per account**, each with its own config file, database and two lock files
(`.lock` and `.lock.run`).
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
  in its own systemd unit or container). For systemd, see [docs/SYSTEMD.md](docs/SYSTEMD.md); for Docker,
  see [Running in Docker](#running-in-docker).

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

## Running in Docker

A container image (distroless, non-root, read-only root filesystem) and a compose file run `quietbox run`
always-on, for example on a NAS: one container per account, the config mounted read-only, the database in
`./data` on a local disk, the password as a compose secret read through `password_file`.

```sh
cd deploy                                  # then: config/, data/, secrets/ as in docs/DEPLOY.md
cp .env.example .env                       # set QUIETBOX_IMAGE=quietbox:local there for a local build
docker compose -f docker-compose.yml -f docker-compose.build.yml build     # the override only for building
docker compose run --rm quietbox init --yes --config /config/quietbox.toml # or migrate an existing database
docker compose up -d
```

See **[docs/DEPLOY.md](docs/DEPLOY.md)** for the setup, password options, migrating an existing database,
backups, updates and troubleshooting. `make docker-build` builds `quietbox:local`; `make docker-buildx`
builds amd64 + arm64.

## Development

```sh
make test     # go test ./... -race
make lint     # go vet + golangci-lint (fetched via `go run`, pinned version)
make build    # static binary in ./bin (CGO_ENABLED=0)
make release-snapshot   # the release archives for all platforms into ./dist, published nowhere
make itest    # starts the throwaway Dovecot server, seeds it, runs integration tests, tears down
```

`make itest` uses `docker compose`, or `podman compose` when there is no `docker`
(override with `COMPOSE=...`). The test server is Dovecot 2.4.5, pinned by digest, bound to
`127.0.0.1:1143`, with mail on tmpfs. Seed mail lives in `testdata/seed/`. Tests never touch a
real mailbox.

Note: Dovecot 2.4.5 does not advertise `OBJECTID`, so the local tests only exercise the
`Message-ID` key fallback. The `EMAILID` path needs a server that supports RFC 8474 (e.g. Fastmail).

## License

MIT, see [LICENSE](LICENSE).
