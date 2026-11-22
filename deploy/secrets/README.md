# secrets/

Put the IMAP (app) password here as `imap_password`: the password alone, on one line.
Everything in this folder except this README is ignored by git.

```sh
scp laptop:path/to/password-file imap_password   # or create it with an editor
chmod 600 imap_password
sudo chown 1026:100 imap_password   # your PUID:PGID from ../.env (`id` on the NAS)
```

Avoid typing the password on a command line (shell history). If you must, use
`umask 077; printf '%s\n' 'the-app-password' > imap_password`.

The container reads it as `/run/secrets/imap_password`. A file the container user cannot
read fails with "permission denied"; see docs/DEPLOY.md, "Passwords".
