# Releasing

1. Merge to `main` and wait for CI to be green.
2. Tag from that commit and push the tag: `git tag v0.1.2 && git push origin v0.1.2`.
3. `release.yml` runs goreleaser and creates a **draft** GitHub release with the archives and `checksums.txt`,
   and commits the Homebrew formula to [michele/homebrew-quietbox](https://github.com/michele/homebrew-quietbox).
   `docker.yml` publishes the container image.
4. Read the draft, then publish it with **"pre-release" unchecked** and "latest release" checked
   (`gh release edit vX.Y.Z --draft=false --latest`). `install.sh` finds the newest version through
   `/releases/latest`, which ignores drafts and pre-releases.
5. Publish promptly: until the draft is published, `brew install` fails because the archives answer 404.

## Homebrew token

The formula is pushed with a token of your own, because the workflow's `GITHUB_TOKEN` can only write to this repository.

1. GitHub > Settings > Developer settings > Personal access tokens > **Fine-grained tokens** > Generate new token.
2. Resource owner: you. Repository access: **Only select repositories**, `michele/homebrew-quietbox`.
3. Permissions, Repository: **Contents: Read and write** (Metadata: Read is added automatically). Nothing else.
4. Set an expiry and note the date: when it lapses, the release still succeeds up to the formula step, then fails.
5. Add it to this repository as an Actions secret named `HOMEBREW_TAP_GITHUB_TOKEN`
   (`gh secret set HOMEBREW_TAP_GITHUB_TOKEN`).

## Notes

- `brews` in `.goreleaser.yaml` is deprecated by goreleaser (casks replace it for macOS apps), so
  `goreleaser check` exits non-zero. It still works; Homebrew casks do not cover Linux, so the formula stays.
- `make release-snapshot` writes the formula to `dist/homebrew/quietbox.rb` without publishing it.
