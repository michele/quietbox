# Contributing to quietbox

Thanks for looking. quietbox is a small project, so a short conversation before a big change
saves everyone time: open an issue first for anything beyond a fix or a small improvement.

## Setup

You need Go 1.26 or newer. The integration tests also need Docker (or Podman with a
compatible compose) for the Dovecot test server.

```sh
make test      # go test ./... -race
make lint      # go vet ./... && golangci-lint run
make itest     # integration tests against the Dovecot server in docker-compose.test.yml
make build     # static binary in ./bin
```

## Where things live

```
cmd/quietbox/        cobra commands only: parse flags, call the engine
internal/mail/       Backend interface and Message/Folder types (no IMAP imports)
internal/imapbackend IMAP implementation (go-imap v2)
internal/classify/   pure functions: (message, sender stats, rules) -> decision. No I/O.
internal/learn/      pure functions: filing history -> sender routes and domain defaults. No I/O.
internal/store/      SQLite state: senders, decisions, training, audit log
internal/engine/     orchestration: sync, classify, act, record
```

`classify` and `learn` stay pure so they can be table-tested exhaustively. All mailbox
changes go through `engine`, never straight from a command. [docs/PLAN.md](docs/PLAN.md)
describes the design and what is and is not verified.

## Rules a change must respect

1. Never expunge or permanently delete mail. "Delete" means move to Trash.
2. Every mutation supports `--dry-run` and is written to the audit log before it runs.
3. `quietbox undo` must be able to reverse any move in the audit log.
4. Tests never touch a real mailbox. Integration tests use the Docker test server only.
5. Never log message bodies or credentials.

## Pull requests

- One logical change per commit, with a conventional commit message (`feat:`, `fix:`,
  `docs:`, `test:`, `build:`...).
- Write the tests with the change. `make test` and `make lint` must pass.
- A change to behaviour or configuration also updates the README or `docs/PLAN.md`.
- If you touch anything that moves mail, run it against the test server (`make itest`) and
  say what you ran. If you could not verify something, say so rather than leaving it implied.
- Ask before adding a dependency. The project keeps a short list on purpose: a static,
  CGO-free binary.

## Provider reports

quietbox has been tried on Fastmail and Gmail only. Reports from other IMAP servers are very
welcome: include the output of `quietbox check` (no credentials) and what worked or did not.
