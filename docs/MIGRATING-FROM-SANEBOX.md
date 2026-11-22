# Migrating from SaneBox

quietbox does the one job of SaneBox that most people use it for: it keeps non-urgent mail out of
your inbox and learns from the folders you move mail into. This guide moves you over without
losing what SaneBox has taught your mailbox.

It is written from quietbox's own documented behaviour. **It has not been tried against a live SaneBox
account**, so read each step before you run it, and use the dry runs.

## What carries over, and what does not

- **Carries over: your filing history.** SaneBox files mail into folders (`SaneLater`, `SaneNews`, `SaneBulk`,
  `SaneBlackHole`) and those folders stay in your mailbox after you leave. `quietbox init` reads them and learns
  which sender goes where, plus domain defaults, so quietbox starts with SaneBox's answers for everyone it has
  already seen.
- **Does not exist in quietbox:** snooze and reply reminders (other `Sane*` folders SaneBox may have created for
  those are not used by quietbox: leave them alone), a web interface, digests, and anything that needs a hosted
  service. quietbox runs on your own machine and reads message **headers only**, never bodies.
- **Different from what you may expect:** blackholed mail is not deleted. It stays in the blackhole folder for
  30 days (`[blackhole] retention`) and then moves to Trash; quietbox never deletes or expunges anything.

## 1. Keep the folder names SaneBox uses

quietbox learns from the folders named in its config. Use SaneBox's names for them, not quietbox's `+Quiet*`
defaults, or `init` finds empty folders and learns nothing:

```toml
[imap]
host = "imap.fastmail.com"          # your server
username = "you@example.com"

[[folder]]
name = "INBOX"
type = "important"

[[folder]]
name = "SaneLater"
type = "later"

[[folder]]
name = "SaneNews"
type = "newsletter"

[[folder]]
name = "SaneBulk"
type = "bulk"

[[folder]]
name = "SaneBlackHole"
type = "blackhole"
```

Start from [`examples/`](../examples) for your provider (Fastmail, Gmail, any IMAP server) and replace the
folder names. Keep your own folder names if you have renamed them. You can rename the folders later on the
server, then change the config and run `quietbox init` again.

**Mail already in `SaneBlackHole`:** it moves to Trash 30 days after quietbox first sees it. If you would rather
review it first, set `[blackhole] retention = "off"` for now.

## 2. Learn while SaneBox is still on (read-only)

```sh
quietbox check                 # connects; the server must offer MOVE and UIDPLUS
quietbox init --dry-run        # what would be learned
quietbox init                  # learns from your folders; moves nothing
quietbox senders --folder SaneNews
quietbox eval                  # how often quietbox agrees with where SaneBox put recent mail
quietbox scan                  # what quietbox would do with the mail in INBOX now
```

`eval` is the number to look at. It learns only from mail older than a cutoff and compares what quietbox would
have done with the newer mail against where that mail really is, which is where SaneBox put it. It also lists
**hidden important**: INBOX mail quietbox would have filed away, the mistake that matters most.

None of these commands changes your mailbox. Do **not** run `once` or `run` yet.

## 3. Switch SaneBox off, then start quietbox

**Do not run quietbox's `once` or `run` while SaneBox is still filing mail.** quietbox cannot tell SaneBox's
moves from yours: mail that SaneBox puts into a folder would be learned as "you moved this there".

1. Turn SaneBox off in SaneBox's own settings (see their documentation). Leave the `Sane*` folders where they are.
2. Run `quietbox init` again. This learns from the final state of the folders and sets the point from which quietbox
   looks at new mail, so nothing SaneBox did before you switched it off is mistaken for your moves.
3. `quietbox once --dry-run`: what quietbox would do with mail that arrived since. Read the reasons.
4. `quietbox once`, then keep an eye on it for a few days. For a daemon use `quietbox run`
   ([systemd](SYSTEMD.md) or [Docker](DEPLOY.md)); for cron use `quietbox once`.

## 4. The first days

- **A mistake?** `quietbox undo --since 1h` puts back what quietbox filed in the last hour (`--since` takes
  `h`, `d`, `w`, `mo`, `y`). To change the future, just move a message into the right folder (or back to INBOX):
  quietbox learns from it, the same way SaneBox did.
- **Why did that happen?** `quietbox explain <uid>` shows the rule, the reason and the signals for one message.
- Mail you have read or flagged is left where it is by default (`skip_seen`, `skip_flagged`).
- Anything quietbox cannot place stays in INBOX: it never hides mail it knows nothing about.
- To go back to SaneBox: stop quietbox, `quietbox undo --since 7d` if you want its moves reverted, turn SaneBox on.

## Not verified

This guide has not been run with a real SaneBox account. The folder names are the four used in this project's
test fixtures. Everything quietbox does here has been run only against a test server (Dovecot), not Fastmail or Gmail.
