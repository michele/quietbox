# Running quietbox in Docker (NAS, home server)

This guide runs `quietbox run`, the always-on daemon, in a container: on a NAS (Synology,
QNAP, Unraid, TrueNAS), a Raspberry Pi or any Linux box with Docker or Podman. Everything
quoted below as output was produced by the image against the Dovecot test server, unless the
section says otherwise; see [Not verified](#not-verified) for what was not tried.

Files: [`Dockerfile`](../Dockerfile), [`deploy/docker-compose.yml`](../deploy/docker-compose.yml),
[`deploy/docker-compose.build.yml`](../deploy/docker-compose.build.yml),
[`deploy/quietbox.example.toml`](../deploy/quietbox.example.toml), [`deploy/.env.example`](../deploy/.env.example).

## How it fits together

- **One container per mailbox account.** Each runs `quietbox run --config /config/<name>.toml`, has
  its own config file, its own data directory (one SQLite database plus two empty lock files,
  `<db>.lock` and `<db>.lock.run`) and its own password secret. Two accounts = two services.
- `run` waits on INBOX with IMAP IDLE and files new mail as it arrives. It only makes outgoing
  connections to your IMAP server: no ports are published.
- The container is stateless apart from `/data`. The image holds only the binary
  (`/quietbox`, static, on distroless `static-debian12:nonroot`: CA certificates and tzdata, no shell).
- **Locks need a local filesystem.** quietbox keeps processes on one mailbox from acting at the same time
  with `flock(2)` on the lock files next to the database. Keep `/data` on a disk of the NAS itself (a
  normal Docker bind mount or volume), **not** on an NFS or SMB share: `flock` there can be unreliable
  and quietbox does not detect it.
- **Never run two daemons on one mailbox from two databases.** The lock is per database file. A laptop
  running `quietbox once`/`run` with its own copy of the database and the NAS container do not see each
  other's locks or audit logs: each treats the other's moves as moves *you* made and trains on them. This
  was reproduced while writing this guide: a NAS copy taken before the laptop filed one more message
  logged `trained promo@deals.test: you moved a message to SaneBulk (route was SaneLater)`, a training
  event nobody asked for. After [migrating](#migrating-an-existing-database), stop quietbox on the
  laptop for good (remove its cron job or service).

## Prerequisites

- Docker with the compose plugin (`docker compose version`), or Podman with `podman compose`.
  Synology DSM 7.2+: Container Manager. Portainer works too (see [NAS notes](#nas-notes)).
- An app password for the mailbox (see [Passwords](#passwords)).
- A working config. If you have used quietbox on a laptop, you already have one; if not, start from
  [`deploy/quietbox.example.toml`](../deploy/quietbox.example.toml) and the README.
- amd64 or arm64 CPU (most NAS and the Raspberry Pi 4/5 with a 64-bit OS). 32-bit ARM is not built.

## Get the image

Pick one.

1. **Registry** (default in the compose file): `ghcr.io/michele/quietbox:latest`, or a version tag.
   The image is published by [`.github/workflows/docker.yml`](../.github/workflows/docker.yml), which runs
   only when a `v*` tag is pushed (or by hand from the Actions tab). **Until a tag is pushed, nothing is
   published and `docker compose pull` fails**: use option 2 meanwhile.
2. **Build locally with the override** (needs a checkout of the repo on the machine that builds):

   ```sh
   cd deploy
   docker compose -f docker-compose.yml -f docker-compose.build.yml build    # tags quietbox:local
   docker compose -f docker-compose.yml -f docker-compose.build.yml up -d
   ```

   or `make docker-build` from the repo root (same `quietbox:local` tag), then set
   `QUIETBOX_IMAGE=quietbox:local` in `deploy/.env`.
3. **Multi-arch build** (build on a laptop, run on the NAS): `make docker-buildx IMAGE=registry.example/quietbox:1 PUSH=1`
   builds `linux/amd64` and `linux/arm64` and pushes them. The build stage cross-compiles, so no QEMU is needed.
   Without a registry, build for the NAS architecture and copy the image over:

   ```sh
   docker build --platform linux/amd64 -t quietbox:local .       # or linux/arm64
   docker save quietbox:local | ssh nas docker load
   ```

Base images are pinned by digest in the `Dockerfile`; bump them deliberately.

## Layout on the host

```
/volume1/docker/quietbox/          # anywhere on a LOCAL disk; this is the compose project folder
├── docker-compose.yml             # from deploy/
├── .env                           # from deploy/.env.example (PUID, PGID, TZ, QUIETBOX_IMAGE)
├── config/quietbox.toml           # your config, mounted read-only at /config
├── data/                          # quietbox.db + .lock + .lock.run (+ learn-report.csv), at /data
└── secrets/imap_password          # the password alone on one line, at /run/secrets/imap_password
```

```sh
mkdir -p /volume1/docker/quietbox/{config,data,secrets}
cd /volume1/docker/quietbox
cp /path/to/repo/deploy/docker-compose.yml /path/to/repo/deploy/.env.example .
cp .env.example .env
cp /path/to/repo/deploy/quietbox.example.toml config/quietbox.toml   # then edit
```

Create `data/` yourself before the first start: a directory Docker creates for a missing bind mount is
owned by root and the container user cannot write to it.

## Configuration

The config is the normal quietbox TOML file (see the README) with two container settings:

```toml
[imap]
host = "imap.fastmail.com"
username = "you@example.com"
password_file = "/run/secrets/imap_password"

[state]
path = "/data/quietbox.db"
```

`deploy/.env` sets:

| Variable | Default | Meaning |
|---|---|---|
| `PUID`, `PGID` | `65532` | uid/gid the container runs as. Set them to the owner of `data/` and `secrets/` (see below). |
| `TZ` | `UTC` | IANA time zone for the times `undo` and `explain` print (`2026-11-22 08:40`); the image has tzdata. |
| `QUIETBOX_IMAGE` | `ghcr.io/michele/quietbox:latest` | image to run; `quietbox:local` for a local build. |

The compose file sets `working_dir: /data` because `init` writes `learn-report.csv` into the current
directory, and the rest of the filesystem is read-only. Without it (for example in your own `docker run`):

```
error: write report: open learn-report.csv: read-only file system
```

and a real `init` would exit with an error **after** saving what it learned. Pass `--report /data/learn-report.csv`
(or `--report ''`) if you run the image another way.

**Restart after editing the config**: `docker compose restart quietbox` (the daemon keeps the config it
started with).

## Passwords

Use an **app password**, never your main account password: Fastmail: Settings > Privacy & Security >
Integrations > New app password, access "Mail (IMAP)". Gmail: turn on 2-step verification, then
Google Account > Security > App passwords (IMAP must be enabled in Gmail settings). An app password
can be revoked on its own if the NAS is ever compromised.

Ranked, best first:

### 1. Compose `secrets:` + `password_file` (recommended)

What the compose file does. The password lives in one file on the host, outside the config, outside
the image and outside the container's environment (so not in `docker inspect`), and is mounted
read-only at `/run/secrets/imap_password`.

```sh
cd /volume1/docker/quietbox
umask 077
printf '%s\n' 'the-app-password' > secrets/imap_password     # or: scp laptop:the-file secrets/imap_password
chmod 600 secrets/imap_password
id                                    # e.g. uid=1026(you) gid=100(users): put these in .env
sudo chown -R 1026:100 data secrets   # PUID:PGID own the data and the secret
```

(Typing the password on the command line leaves it in the shell history; `scp`, or an editor, avoids
that. `history -d` or a leading space with `HISTCONTROL=ignorespace` also work.)

**The uid pitfall.** Outside Swarm, a compose file secret is a bind mount of the host file: the container sees
it with its host owner and mode (do not count on the compose `uid`/`gid`/`mode` secret options to change that;
they are a Swarm feature). The image
runs as uid 65532 by default, so a `0600` file owned by your NAS user is unreadable inside:

```
error: imap.password_file /run/secrets/imap_password: open /run/secrets/imap_password: permission denied
```

`run` stops on this at start and the restart policy starts it again, so it shows up as a restart loop
(see [Troubleshooting](#troubleshooting)). Fix it one of two ways:

- **Run as the owner (preferred):** set `PUID`/`PGID` in `.env` to the owner of `secrets/imap_password`,
  and own `data/` by the same uid. The file stays `0600`.
- **Shared group:** keep `PUID=65532`, give the file a group the container is in and make it group-readable:
  `chgrp 100 secrets/imap_password && chmod 0440 secrets/imap_password`, `PGID=100`. quietbox refuses a
  group- or world-readable password file outside a container, but inside one it only warns, on every
  command that reads the password:

  ```
  warning: password file /run/secrets/imap_password is readable by other users (mode 0440); fine for a mounted container secret, not elsewhere
  ```

  Container detection uses `/.dockerenv`, `/run/.containerenv` or `KUBERNETES_SERVICE_HOST`. If it guesses
  wrong, set `QUIETBOX_CONTAINER=1` in the service's `environment:`. With `QUIETBOX_CONTAINER=0` the
  outside-a-container rule applies and the same file is refused:

  ```
  error: password file /run/secrets/imap_password is readable by other users (mode 0440): run `chmod 600 /run/secrets/imap_password`
  ```

The same uid must also be able to write `data/`; otherwise, with a database already there:

```
error: stat /data/quietbox.db: permission denied
```

### 2. `env_file` with `QUIETBOX_IMAP_PASSWORD`

Simple, but the password is in the container's environment: visible to anyone who can run
`docker inspect` on the host, in `/proc/<pid>/environ` and to child processes. Never write the
password inline under `environment:` in the compose file (that file gets copied, shared and committed).

```sh
umask 077; printf 'QUIETBOX_IMAP_PASSWORD=%s\n' 'the-app-password' > quietbox.env; chmod 600 quietbox.env
```

```yaml
services:
  quietbox:
    env_file: [quietbox.env]
    # and remove `secrets:` here and `password_file` from the config
```

With neither `password_file` nor `password_command` in the config, the variable is used.

### 3. `password_command`

Runs a command through `sh -c` (30 s limit) and uses its single line of output. The default image has
no shell and no secret tool, so this is for a **custom image** only, for example:

```dockerfile
FROM ghcr.io/michele/quietbox:latest AS quietbox
FROM alpine:3.22
RUN apk add --no-cache your-secret-cli   # e.g. a Vault, Bitwarden or 1Password CLI
COPY --from=quietbox /quietbox /quietbox
USER 65532:65532
ENTRYPOINT ["/quietbox"]
CMD ["run", "--config", "/config/quietbox.toml"]
```

with `password_command = "your-secret-cli get imap-password"` in the config. That tool needs its own
credentials, which moves the problem rather than solving it on a single NAS; option 1 is simpler.

### Other platforms

- **Docker Swarm:** `docker secret create imap_password -` and `secrets: [imap_password]` with
  `external: true`; Swarm honours the `uid`/`gid`/`mode` options, so `uid: "65532"`, `mode: 0400` works.
- **Kubernetes:** a `Secret` mounted as a volume at `/run/secrets` (`defaultMode: 0400`, `runAsUser: 65532`
  or `fsGroup`), `password_file = "/run/secrets/imap_password"`.
- **systemd without Docker:** `LoadCredential=imap:/etc/quietbox/imap.pw` (or `LoadCredentialEncrypted=`
  after `systemd-creds encrypt`) with `password_file = "${CREDENTIALS_DIRECTORY}/imap"`; see the README.

### Rotating the password

1. Create the new app password at the provider (keep the old one for now).
2. Write it **into the existing file**: `printf '%s\n' 'new-password' > secrets/imap_password`.
   This keeps the file's inode, owner and mode.
3. `docker compose restart quietbox`, then check the log for `pass done`.
4. Revoke the old app password.

Why the restart: `run` re-reads `password_file` on every **reconnect** (verified: after a wrong
password it logged `error: login as ... failed: ... AUTHENTICATIONFAILED ...; reconnecting in 5s` and
picked up the corrected file at the next attempt, without a restart), but a healthy connection stays
logged in with the old password until something reconnects it. Restarting applies the new one now. If you
replace the file instead of rewriting it (`mv`, many editors, `scp`), the running container keeps the old
file through its bind mount until it is restarted; a restart re-reads the path (checked with Podman: the
replaced file took effect after `restart`).

### Backing up the secret

The password is not needed to restore quietbox: you can always create a new app password. If you back
the folder up, exclude `secrets/` or keep the backup encrypted; never commit it. This repository's
`.gitignore` ignores `deploy/secrets/*`, `deploy/config/`, `deploy/data/`, `.env` and `deploy/local/`.

## First run on a new install

Skip to [Migrating](#migrating-an-existing-database) if you already use quietbox on another machine.

`init` asks before creating missing folders. `docker compose run` from an SSH session has a terminal and
can answer, but from a script, a NAS task scheduler or Portainer there is none, and without one `init`
skips the creation: pass `--yes` (or create the folders in your mail client first). Every one-shot command
needs `--config` because its arguments replace the default command.

```sh
cd /volume1/docker/quietbox
docker compose run --rm quietbox check --config /config/quietbox.toml         # login, capabilities, folders
docker compose run --rm quietbox init --dry-run --config /config/quietbox.toml
docker compose run --rm quietbox init --yes --config /config/quietbox.toml    # learn; creates missing folders
docker compose run --rm quietbox senders --config /config/quietbox.toml | head
docker compose up -d                                                         # start the daemon
```

For an account no tool ever sorted, use `init --fresh` (and optionally `--backlog 30d`, then
`once --dry-run` and `once`), as in the README's "Starting on a never-managed account".

`init` output ends with `Report written to learn-report.csv`: that is `data/learn-report.csv` on the
host (mode 0600, it lists sender addresses). Starting the daemon before `init` fails at once with
`error: no quietbox database yet: run \`quietbox init\` first`.

Only mail that arrives **after** `init` is ever moved. The first `once` against the test server:

```
SENDER                 TO        RULE    STATUS  WHY
writer1@substack.test  SaneNews  sender  done    sender route to SaneNews (learned: SaneNews 100%)

1 new, 1 moved, 0 stayed in INBOX, 0 skipped (read, flagged, moved once before, moved back by you, or a copy of mail already seen), 0 failed
```

## Migrating an existing database

Keep what quietbox has learned (routes, training, audit log for `undo`) by moving the database instead
of running `init` again.

1. **Stop quietbox on the old machine for good**: stop `quietbox run`, remove the cron entry for
   `quietbox once`. Nothing may write the database during the copy, and nothing may run against the
   mailbox from that copy afterwards (see [How it fits together](#how-it-fits-together)).
2. **Make a consistent copy.** The database uses SQLite WAL mode, so a plain `cp` of a database that some
   process has open can miss the newest data, which lives in `quietbox.db-wal`. Use SQLite's backup, which
   writes one complete file:

   ```sh
   # on the laptop; default paths: config.toml -> quietbox.db, --account NAME -> NAME.db
   sqlite3 ~/.local/state/quietbox/quietbox.db ".backup '/tmp/quietbox.db'"
   sqlite3 /tmp/quietbox.db 'PRAGMA integrity_check;'      # prints: ok
   ```

   Copy only `quietbox.db`. Do not copy `-wal`/`-shm` files (if opening the copy created an empty
   `-wal` next to it, leave it behind) and do not copy the `.lock` files; they are recreated.
   (Without `sqlite3`: once every quietbox process has stopped, the `-wal` file is gone or empty and a plain
   copy of `quietbox.db` is consistent.)
3. **Copy it to the NAS and give it to `PUID:PGID`:**

   ```sh
   scp /tmp/quietbox.db nas:/volume1/docker/quietbox/data/quietbox.db
   ssh nas 'sudo chown 1026:100 /volume1/docker/quietbox/data/quietbox.db && sudo chmod 600 /volume1/docker/quietbox/data/quietbox.db'
   ```

   With `[state] path = "/data/quietbox.db"`, the file name in `data/` must be `quietbox.db`. An
   `--account NAME` database is `NAME.db`: rename it or point `[state] path` at it.
4. **Use the same account in the config.** The database remembers its mailbox (`username@host`) and refuses
   another one. Copy the laptop config, change only `password_file` and add `[state] path`.
5. **Verify** before starting the daemon:

   ```sh
   docker compose run --rm quietbox senders --config /config/quietbox.toml | head -5
   docker compose run --rm quietbox explain --folder SaneNews --config /config/quietbox.toml '<some-message-id@example.com>'
   docker compose run --rm quietbox once --dry-run --config /config/quietbox.toml
   ```

   On the test copy: `senders` listed the learned routes (`campaign@mcsv.test  SaneNews  learned ...`),
   `explain` showed the audit row of a move the old copy had made, and `once --dry-run` ended
   `0 new, 0 would move, ...`, i.e. it continued from where the old copy stopped instead of re-sweeping INBOX.
6. `docker compose up -d`. Keep the laptop copy as a backup, and never run quietbox with it again.

## Day-2 operations

### Logs: is it alive and learning?

```sh
docker compose logs -f --timestamps quietbox
```

The daemon prints a line per folder for each pass and then a summary (quietbox itself does not timestamp
lines; `--timestamps` adds Docker's):

```
quietbox running; press Ctrl-C to stop
SaneNews: 0 new message(s), 0 moved there by you
INBOX: 1 new message(s)
pass done: 1 new, 1 moved, 0 failed
trained promo@deals.test: you moved a message to SaneBulk (route was SaneLater)
trained 1 sender(s)
```

A pass runs when mail arrives and, without mail, at the IDLE refresh about every 20 minutes, so a healthy
daemon logs `pass done` at least that often. `trained ...` lines are the moves you made by hand being
learned. `error: ...; reconnecting in 5s` (growing to 5 min) is a lost connection or a failed login being
retried. `docker compose ps` shows `Up` (or `Restarting`, see Troubleshooting).

**No healthcheck.** The image has no shell, and the only probe quietbox has, `check`, logs in to IMAP,
which is too heavy to run every 30 seconds and would turn a provider outage into "unhealthy". A daemon that
dies exits, and `restart: unless-stopped` starts it again; a stuck daemon shows up as missing `pass done`
lines (and `still waiting for the pass lock after 15m` if a lock is held).

### Commands next to the daemon

Use `docker compose exec` (inside the running container) or `docker compose run --rm` (a throwaway
container with the same mounts). Both share the database and the locks with the daemon:

```sh
docker compose exec quietbox /quietbox senders --config /config/quietbox.toml
docker compose exec quietbox /quietbox explain --config /config/quietbox.toml '<id@example.com>'
docker compose run --rm quietbox senders set promo@deals.test SaneBulk --config /config/quietbox.toml
docker compose run --rm quietbox undo --since 1h --dry-run --config /config/quietbox.toml
docker compose run --rm quietbox undo --since 1h --config /config/quietbox.toml
```

`run` holds the pass lock only while a pass executes, so these complete at once while it idles and
otherwise wait for the pass to end (`waiting for another quietbox...`). Verified: `senders set` with the
daemon up printed `promo@deals.test -> SaneBulk (trained)` and the daemon's next pass used the route.
Do not start a second `run` (`docker compose run --rm quietbox` with no arguments): it is refused with
`another quietbox run is already running on this mailbox`.

### Updating

```sh
# 1. back up the database first (below): schema migrations run automatically when a new version opens it
docker compose pull                    # registry image; or rebuild with the override
docker compose up -d                   # recreates the container; SIGTERM stops the old one cleanly
docker compose logs --tail 20 quietbox
```

Pin a version tag (`QUIETBOX_IMAGE=ghcr.io/michele/quietbox:1.2.3`) instead of `latest` if you want
updates only when you choose. Rolling back to an older image after a migration may not work: restore the
backup taken in step 1.

### Backing up the database

Simplest, with a few seconds of downtime (a cleanly stopped quietbox leaves no `-wal` file, so the copy is
consistent):

```sh
docker compose stop quietbox && cp -p data/quietbox.db backups/quietbox-$(date +%F).db && docker compose start quietbox
```

Online, with the daemon up, through a throwaway container (`su-exec` runs `sqlite3` as `PUID:PGID`; running
it as root could leave root-owned `-wal`/`-shm` files the daemon cannot open):

```sh
docker run --rm -v "$PWD/data:/data" -v "$PWD/backups:/backups" alpine:3.22 \
  sh -c 'apk add -q sqlite su-exec && su-exec 1026:100 sqlite3 /data/quietbox.db ".backup /backups/quietbox.db"'
```

(`backups/` must exist and be writable by that uid. On Synology, a Hyper Backup task over the
`docker/quietbox/backups` folder then takes it off the box.) Restore: stop the service, put the file back as
`data/quietbox.db` owned by `PUID:PGID`, start.

### Stopping

`docker compose stop` (or `down`) sends SIGTERM; `run` ends cleanly and exits 0, well within the 30 s
`stop_grace_period` (0.3 s in the test). A pass in progress when it is killed anyway is settled on the next
start (the audit log is written before every move).

### Resources

Idle on the test mailbox: about 2 MB RAM, 7 threads, no CPU. A pass reads the database's message table, so
memory grows with the mailbox; the `mem_limit: 512m` and `pids_limit: 128` are generous caps, not measurements
of a large real mailbox. Raise `mem_limit` if the container is ever OOM-killed (`docker inspect` shows
`OOMKilled: true`).

### Time zone

`TZ` affects the times quietbox prints (`undo`, `explain`), not what it does; it compares message dates in
UTC either way. Checked: the same audit row printed `07:40` with `TZ=UTC` and `08:40` with `TZ=Europe/Rome`.
The container clock is the host clock: keep NTP on.

## Several accounts

Copy the service: a second service with its own `command` config file, its own data directory and its own
secret. YAML anchors keep it short:

```yaml
services:
  quietbox:          &qb   # ... the service from deploy/docker-compose.yml ...
  quietbox-work:
    <<: *qb
    command: ["run", "--config", "/config/work.toml"]
    volumes:
      - ./config:/config:ro
      - ./data/work:/data
    secrets:
      - source: work_imap_password
        target: imap_password
secrets:
  work_imap_password:
    file: ./secrets/work_imap_password
```

(`<<: *qb` merges the mapping, but lists such as `volumes` are replaced, not merged, so repeat them.)
`work.toml` uses `[state] path = "/data/work.db"` and the same `password_file`. One-shot commands name
the service and its config: `docker compose run --rm quietbox-work init --yes --config /config/work.toml`.

## NAS notes

- **Paths:** use a folder on a local volume, e.g. Synology `/volume1/docker/quietbox`, QNAP
  `/share/Container/quietbox`, Unraid `/mnt/user/appdata/quietbox` (or a direct pool path such as
  `/mnt/cache/appdata/quietbox`). Never a mounted NFS/SMB share.
- **PUID/PGID:** SSH in as the user who owns the folder and run `id`. On Synology the first user is
  often `uid=1026`, group `users` = `100`; check, do not assume. Put the values in `.env` and `chown -R PUID:PGID data secrets`.
- **Synology Container Manager:** create the folder with File Station (`docker/quietbox` with `config`,
  `data`, `secrets`), upload the files (the secret via SSH/`scp` is better than the web UI), then
  Container Manager > Project > Create, path `/volume1/docker/quietbox`, source "Use existing
  docker-compose.yml". Run the one-shot `init` over SSH (`sudo docker compose run --rm ...` in that folder)
  before starting the project.
- **Portainer:** Stacks > Add stack, paste the compose file, set the `.env` values as stack environment
  variables. Relative paths like `./data` resolve inside Portainer's own data directory, so use absolute paths
  (`/volume1/docker/quietbox/data`) in a pasted stack.
- **Generic SSH:** `cd /volume1/docker/quietbox && docker compose up -d`.
- **SELinux hosts** (Fedora, RHEL): add `:Z` to the bind mounts (`./data:/data:Z`) or the container may be
  denied access. NAS systems usually do not use SELinux.

## Security hardening

What the compose file sets, and why:

| Setting | Effect |
|---|---|
| `user: PUID:PGID` (image default `65532:65532`, "nonroot") | quietbox never runs as root. |
| `read_only: true` + `tmpfs: /tmp` | the image cannot be modified; only `/data` (and a 64 MB `/tmp` in RAM for SQLite scratch files) is writable. |
| `cap_drop: [ALL]` | no Linux capabilities; quietbox needs none (it binds no port, changes no ownership). |
| `security_opt: no-new-privileges:true` | nothing in the container can gain privileges through setuid binaries (there are none either). |
| no `ports:` | nothing listens; the only traffic is the outgoing IMAP connection. |
| `/config` read-only, password as a secret file | a compromised process cannot rewrite its config, and the password is not in the environment. |
| `mem_limit`, `pids_limit`, log rotation | a runaway process cannot take the NAS down or fill its disk with logs. |
| distroless base, pinned by digest | no shell, no package manager, no surprise base-image changes. |

Checked on the running container: user `65532:65532`, read-only root filesystem, every capability dropped,
`no-new-privileges`, `/config` and `/run/secrets/imap_password` mounted read-only, `/data` read-write.

## Troubleshooting

- **Restart loop** (`docker compose ps` says `Restarting`, or `Up Less than a second`): a fatal error at
  start. Read it with `docker compose logs --tail 20 quietbox`; it is the `error:` line, repeated. Common ones:
  - `error: imap.password_file /run/secrets/imap_password: open /run/secrets/imap_password: permission denied`:
    the [uid pitfall](#1-compose-secrets--password_file-recommended).
  - `error: stat /data/quietbox.db: permission denied` (or an error opening the database): `data/` is not
    writable by `PUID:PGID`; `chown -R PUID:PGID data`.
  - `error: no quietbox database yet: run \`quietbox init\` first`: run `init` (or migrate) before `up`.
  - a config error (unknown key, missing folder type): fix `config/quietbox.toml`, then `restart`.
  - `another quietbox run is already running on this mailbox (lock file /data/quietbox.db.lock.run)`: two daemons
    share one data directory (two services pointing at the same `data/`, or a leftover container). One per database.

  Docker waits a growing delay between restarts; Podman restarted the failing test container about 200 times
  in 8 seconds, so stop it (`docker compose stop`) while you fix things.
- **`waiting for another quietbox...`** from a one-shot command: a pass is in progress; it ends by itself.
  If it does not, `docker compose logs` shows what the daemon is doing; `once`/`undo`/`init` give up after
  `--lock-timeout` (default 2m). An `init` without `--yes` sitting at its question in a `docker compose run`
  holds the lock (and stalls the daemon's passes): answer it or Ctrl-C.
- **`error: login as ... failed: ... AUTHENTICATIONFAILED ...; reconnecting in 5s`**: wrong or revoked password.
  Fix the file in place; the next attempt picks it up.
- **`warning: password file ... is readable by other users`**: expected with the shared-group setup; `chmod 600`
  plus `PUID` = owner makes it go away. `QUIETBOX_CONTAINER=1` / `0` overrides container detection.
- **`... was renumbered by the server (UIDVALIDITY changed): ...; run \`quietbox init\` to relearn`**: the server
  rebuilt a folder (provider migration, restore). Nothing is moved from that folder until you run
  `docker compose run --rm quietbox init --yes --config /config/quietbox.toml`.
- **Times in `undo`/`explain` look wrong**: set `TZ` in `.env`; check the NAS clock.
- **`this database belongs to a different account: it belongs to ... but this configuration is for ...`**: the
  config's `username`/`host` differ from the account the database was created for; use the same account, or give
  this one its own `[state] path`.
- **`tls = "none"` refused**: only allowed for a loopback host; use `implicit` (993) or `starttls` for a real server.

## Not verified

- **A real NAS.** Everything ran with Podman 5.2 (rootful machine) and Docker Compose 2.29 on macOS against the
  Dovecot test server; ownership and permission behaviour was checked on the Linux VM's own filesystem, not on
  Synology/QNAP/Unraid. Synology Container Manager and Portainer steps are written from their documentation, not
  clicked through.
- **A real provider** (Fastmail, Gmail) in the container; memory and pass time on a large mailbox.
- **arm64 vs amd64:** the image was built for both. The arm64 image ran natively; the amd64 image was built by
  cross-compilation and started (`--help`) only under the Podman VM's emulation, not on amd64 hardware.
- **Registry publishing:** the GitHub workflow has not run (no tag pushed); `ghcr.io/michele/quietbox` does not
  exist until it does.
- **Docker Engine itself** (as opposed to Podman behind the Docker Compose CLI): restart backoff, file-secret
  handling and `restart` re-reading a replaced secret file are expected to match but were observed on Podman.
- `flock` across containers was exercised (one-shot containers waited for and coordinated with the daemon) but
  only on one host filesystem; network filesystems remain unsupported.
