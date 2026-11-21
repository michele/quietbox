# Password sources: design

Status: approved in conversation, 2026-11-21.

## Intent
Environment variables leak more than a file (`/proc/<pid>/environ`, `systemctl show`, child processes, crash
dumps, shell history). A daemon on a server must keep the secret somewhere, so offer sources that fit how
servers and laptops store secrets: a file (Docker/Kubernetes secrets, systemd `LoadCredential=`) and a command
(macOS Keychain, `pass`, 1Password CLI, Vault).

## Goals
- `password_file` and `password_command` in `[imap]`; at most one; they win over the environment variables.
- Refuse a group/other-readable password file, except inside a container, where it warns on every command.
- Never log or print the secret. The `password` key stays forbidden.

## Non-goals
OS keychain libraries (no new dependency; `password_command` covers it), OAuth2, encrypting the file ourselves.

## Design
- `[imap] password_file = "PATH"`: `~/` and `$VAR`/`${VAR}` are expanded (a variable that is unset is an error naming
  it, so `${CREDENTIALS_DIRECTORY}/imap` works under systemd); a relative path is relative to the config file's folder.
  The file must be a regular file (symlinks are followed: Docker/K8s secrets are symlinks). Its content is trimmed and
  must be one non-empty line.
- `[imap] password_command = "CMD"`: run with `sh -c`, 30 s timeout, stdout (trimmed, one non-empty line) is the
  password. On failure the error carries the exit status and stderr (never stdout). Config files are trusted input.
- Both set is a config error. Neither set: `QUIETBOX_IMAP_PASSWORD_<NAME>` / `QUIETBOX_IMAP_PASSWORD` as before.
- Permissions: `mode & 077 != 0` on the file is a hard error outside a container ("chmod 600 PATH"). Inside a container
  it is a warning printed to stderr by **every** command that loads the config.
- Container detection: `QUIETBOX_CONTAINER` = `0` (forces "not a container") or any other non-empty value (forces "container"); otherwise `KUBERNETES_SERVICE_HOST`
  set, or `/.dockerenv` or `/run/.containerenv` exists. It is a heuristic; `QUIETBOX_CONTAINER=1` declares it.
- Commands that never connect (`senders`, `accounts`) do not read the password and raise nothing.
- `run` re-reads the password before every (re)connect, so a rotated file or a changed keychain entry needs no restart.
  Other commands read it once.
- Parsing stays pure: files and commands are touched only by `Load`/`LoadAccount` (and `RefreshPassword`).

## Verification
Unit tests with injected filesystem, command runner and environment (the container heuristic must not depend on the
machine the tests run on); a real-file and a real-command test; Dovecot integration tests using both sources.
