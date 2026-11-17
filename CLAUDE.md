# CLAUDE.md

Working name: **quietbox** (rename freely). A self-hosted, open-source alternative to SaneBox:
a Go CLI/daemon that watches an IMAP mailbox and moves non-urgent mail out of the inbox,
learning from the folders the user moves mail into.

Read `docs/PLAN.md` before starting any task. Work on one milestone at a time.

## Stack

- Go (latest stable), single static binary, no CGO
- IMAP: `github.com/emersion/go-imap/v2`
- State: SQLite via `modernc.org/sqlite` (pure Go, keeps the build static)
- CLI: `github.com/spf13/cobra`
- Config: one TOML file plus env vars for secrets (`QUIETBOX_IMAP_PASSWORD`)
- Ask before adding any other dependency.

## Commands

```sh
make test          # go test ./... -race
make lint          # go vet ./... && golangci-lint run
make itest         # integration tests against the IMAP server in docker-compose.test.yml
make build         # static binary in ./bin
```

## Layout

```
cmd/quietbox/        cobra commands only: parse flags, call engine
internal/mail/       Backend interface + Message/Folder types (no IMAP imports here)
internal/imapbackend implementation of mail.Backend using go-imap v2
internal/classify/   PURE functions: (message, sender stats, rules) -> Decision. No I/O.
internal/store/      SQLite: senders, decisions, training events, audit log, schedules
internal/engine/     orchestration: sync -> classify -> act -> record
```

`classify` must stay pure so it can be table-tested exhaustively.
All mailbox mutations go through `engine`, never directly from commands.

## Safety rules (non-negotiable)

1. **Never expunge or permanently delete mail.** "Delete" means move to Trash.
2. Every mutation supports `--dry-run` and is written to the audit log
   (message key, from-folder, to-folder, reason, timestamp) *before* it runs.
3. `quietbox undo --since <duration>` must be able to reverse any move in the audit log.
4. Tests never touch a real mailbox. Integration tests use the docker test server only.
5. Never log message bodies or credentials.

## Identifying messages

IMAP UIDs change when a message moves. Use this stable key:
1. `EMAILID` from the OBJECTID extension (RFC 8474) when the server supports it
2. otherwise the `Message-ID` header
3. otherwise a hash of (From, Date, Subject)

## Definition of done

A task is done only when ALL of these are true:
- unit tests written first (or alongside) and passing with `-race`
- `make lint` is clean
- the feature has been run end to end against the docker test server
  and you have shown the command output
- README / `docs/PLAN.md` updated if behaviour or config changed

If you cannot verify something, say so explicitly. Do not report it as working.

## Working style

- Start each task in plan mode: propose the approach, list the files you'll touch, wait for approval.
- Small commits, one logical change each, conventional commit messages.
- When you make the same mistake twice, propose a line to add to this file.
