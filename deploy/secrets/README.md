# secrets/

Put the IMAP (app) password here as `imap_password`: the password alone, on one line.
Everything in this folder except this README is ignored by git.

```sh
printf '%s\n' 'your-app-password' > imap_password   # or: scp it here from another machine
chmod 600 imap_password
chown "$PUID:$PGID" imap_password   # the uid/gid the container runs as (see ../.env)
```

The container reads it as `/run/secrets/imap_password`. A file the container user cannot
read fails with "permission denied"; see docs/DEPLOY.md, "Passwords".
