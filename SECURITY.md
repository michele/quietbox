# Security policy

quietbox logs in to a mailbox and moves mail, so a security problem here can expose
credentials or misplace someone's email. Please report it privately first.

## Reporting a vulnerability

Use GitHub's private reporting: open the **Security** tab of
[michele/quietbox](https://github.com/michele/quietbox) and choose **Report a vulnerability**
(or go to <https://github.com/michele/quietbox/security/advisories/new>).

Please do not open a public issue for a vulnerability. Include the version
(`quietbox --version`), what you did, what happened, and what you expected. Do not include
real credentials or mail content; redact addresses and subjects.

This is a small project maintained by one person. I will acknowledge a report as soon as I
can and keep you updated, but I cannot promise a fixed response time.

## What counts

Examples of what to report:

- a password, app password or message content showing up in a log, an error message,
  the database, the audit log or a report file
- a way to make quietbox expunge or permanently delete mail (it must only ever move to Trash)
- a way to make it read or write files outside its configuration, state and report paths
- a way to run a command you did not configure (`password_command` is the only one, and
  it comes from your own config file)
- a flaw in the install script, the release archives or their checksums

Behaviour that is not a vulnerability but is still a bug (for example, a message filed in the
wrong folder) belongs in a normal issue.

## Supported versions

Only the latest release gets fixes while the project is at version 0.x.

## Things worth knowing

- Passwords never go in the config file. Use `password_file`, `password_command` or an
  environment variable. quietbox refuses a password file readable by other users outside a container.
- The macOS binaries are not signed or notarized.
- quietbox keeps headers and message keys only, never bodies, but its SQLite database
  does hold sender addresses. Protect the state directory like you protect the mailbox.
