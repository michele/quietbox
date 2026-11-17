# quietbox

A self-hosted, open-source alternative to SaneBox: a Go CLI/daemon that watches an IMAP
mailbox and moves non-urgent mail out of the inbox, learning from the folders you move mail into.

**Status: M0 (read-only skeleton).** Only `quietbox check` exists. quietbox does not move
or change any mail yet. See [docs/PLAN.md](docs/PLAN.md) for the roadmap.

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

## Usage

```sh
make build
QUIETBOX_IMAP_PASSWORD=... ./bin/quietbox check --config path/to/config.toml
```

`check` connects, prints the server capabilities, says whether stable `EMAILID` message keys
are available (otherwise it falls back to the `Message-ID` header), and shows which configured
folders exist, their message counts and which folder is the detection target for each type.
It is read-only.

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
