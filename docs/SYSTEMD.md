# Running quietbox with systemd

Runs `quietbox run`, the always-on daemon, as a systemd service on a Linux server, VPS or home box.
One service instance per mailbox account: `quietbox@fastmail`, `quietbox@work`, ...
(For a container instead, see [DEPLOY.md](DEPLOY.md).)

Files in [`deploy/systemd/`](../deploy/systemd) (also inside the release archives):

| File | Install as |
|---|---|
| `quietbox@.service` | `/etc/systemd/system/quietbox@.service` |
| `quietbox.sysusers` | `/etc/sysusers.d/quietbox.conf` |
| `user/quietbox@.service` | `~/.config/systemd/user/quietbox@.service` (the no-root variant, see the end) |

## Layout

| What | Where |
|---|---|
| binary | `/usr/local/bin/quietbox` |
| user and group | `quietbox` (a system user, no login) |
| config for account `NAME` | `/etc/quietbox/NAME.toml` (no secrets in it) |
| IMAP password | `/etc/quietbox/NAME.password` (owner `quietbox`, mode `0600`) |
| database | `/var/lib/quietbox/NAME.db` (+ the learn report and two lock files next to it) |

The unit uses a plain `quietbox` user, not `DynamicUser`, on purpose: you will run `undo`, `senders`
and `once` next to the daemon, and they must open the same database as the same user. Run as root,
they would leave root-owned SQLite files in the state directory that the service could not write.

## Install

Take the archive for your machine from the releases page, then, as root:

```sh
install -m 0755 quietbox /usr/local/bin/quietbox
quietbox --version

install -D -m 0644 deploy/systemd/quietbox.sysusers /etc/sysusers.d/quietbox.conf
systemd-sysusers                                   # creates the quietbox user and group
install -d -o quietbox -g quietbox -m 0700 /var/lib/quietbox
install -d -m 0755 /etc/quietbox
install -m 0644 deploy/systemd/quietbox@.service /etc/systemd/system/quietbox@.service
systemctl daemon-reload
```

## Set up an account

Say the account is `fastmail`. Write `/etc/quietbox/fastmail.toml` (start from
[`examples/`](../examples), and see the [README](../README.md#configuration)). Two settings must match
what the unit expects:

```toml
[imap]
host = "imap.fastmail.com"
username = "you@example.com"
password_file = "/etc/quietbox/fastmail.password"

[state]
path = "/var/lib/quietbox/fastmail.db"      # the unit waits for this file to exist
```

The password, one line, readable only by the service user (quietbox refuses a password file that
others can read):

```sh
install -o quietbox -g quietbox -m 0600 /dev/stdin /etc/quietbox/fastmail.password
# paste the app password, Enter, Ctrl-D
```

## First run

Learn from your mailbox as the service user, then start the daemon:

```sh
alias qb='sudo -u quietbox quietbox --config /etc/quietbox/fastmail.toml'

qb check
qb init --dry-run                         # read-only: what would be learned
qb init                                   # learns; moves nothing
qb once --dry-run                         # what the first pass would do
systemctl enable --now quietbox@fastmail
journalctl -u quietbox@fastmail -f
```

Until `init` has created `/var/lib/quietbox/fastmail.db`, the unit does not start: `systemctl start`
says nothing and `systemctl status` shows `inactive (dead)`; the journal has "was skipped because of an
unmet condition check". That is deliberate, so a mailbox nobody has learned yet does not crash-loop.
`run` also refuses to start for other reasons (the server cannot `MOVE`, ...); it then exits with the
reason in the journal, systemd retries after 30 seconds, and gives up after 5 failures in 10 minutes.

## Day to day

- **Logs:** `journalctl -u quietbox@fastmail`. `run` prints one line per pass, per move you taught it and per
  warning. It never prints message bodies or credentials.
- **Commands next to the daemon:** `qb undo --since 1h`, `qb senders ...`, `qb once`, `qb explain ...`. While `run`
  idles they all work; during a pass they wait for it (see "Locks" in the README). `--since` takes `h`, `d`, `w`, `mo`, `y`.
- **After editing the config or replacing the binary:** `systemctl restart quietbox@fastmail`. The daemon keeps
  the config it started with.
- **More accounts:** repeat "Set up an account" with another name and `systemctl enable --now quietbox@work`.
  Each has its own config, password and database.
- **Stopping:** `systemctl stop` sends SIGTERM; `run` stops between messages and exits 0.
- **Backups:** the database is `/var/lib/quietbox/NAME.db`; see "Backing up the database" in [DEPLOY.md](DEPLOY.md#backing-up-the-database)
  (use `sqlite3 .backup`, not `cp`, while the daemon runs).

## Hardening

The unit drops all capabilities, makes the system read-only except its own state directory, hides
`/home`, `/tmp` and devices, allows only IPv4/IPv6 sockets, forbids executable memory and limits system
calls to `@system-service`. `systemd-analyze security quietbox@NAME.service` rates it 1.5 (OK) on systemd 255.
If name resolution fails on an unusual setup (a resolver reached over a Unix socket), add `AF_UNIX` to
`RestrictAddressFamilies=` with a drop-in: `systemctl edit quietbox@fastmail`.

## No root: a user service

`deploy/systemd/user/quietbox@.service` runs the daemon as you, with quietbox's own defaults for
`--account NAME`: config `~/.config/quietbox/NAME.toml`, database `~/.local/state/quietbox/NAME.db`.

```sh
install -D -m 0644 deploy/systemd/user/quietbox@.service ~/.config/systemd/user/quietbox@.service
install -m 0755 quietbox ~/.local/bin/quietbox
quietbox --account fastmail init
systemctl --user daemon-reload
systemctl --user enable --now quietbox@fastmail
loginctl enable-linger "$USER"            # keep it running when you are logged out
journalctl --user -u quietbox@fastmail
```

## Troubleshooting

- **`inactive (dead)` right after `enable --now`:** `init` has not been run (see "First run"), or `[state] path` is not
  `/var/lib/quietbox/NAME.db`.
- **`password file ... is readable by group/others`:** `chmod 600` it and `chown quietbox:quietbox`.
- **`permission denied` on the config or database:** run quietbox commands as `sudo -u quietbox`, not as root.
- **`another quietbox run is already running`:** a second `run` for the same database; stop the other one.

## What was verified

On systemd 255 (Fedora CoreOS 40, aarch64) with the `linux/arm64` release binary and the Dovecot test server:
`systemd-analyze verify` clean for both units; the unit stays down before `init`; after `init` it starts as the `quietbox`
user, files a message delivered while it idles, `undo` run as the same user beside it works, every state file stays
`quietbox:quietbox`, and `systemctl stop` ends it with `Result=success`, exit status 0.

**Not verified:** other distributions or `x86_64`; a real IMAP provider; DNS resolution under `RestrictAddressFamilies`;
starting at boot (`enable` was only checked statically); running the user unit (only `systemd-analyze --user verify`).
