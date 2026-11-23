#!/bin/sh
# quietbox installer: https://quietbox.sh
#
#   curl -fsSL https://quietbox.sh/install.sh | sh
#
# Downloads the release archive for this machine from GitHub, checks it against
# the published checksums.txt, and installs the quietbox binary.
#
# Environment:
#   VERSION      version to install, e.g. 0.1.1 (default: the latest release)
#   INSTALL_DIR  where to put the binary (default: $HOME/.local/bin)
#
# Read it before you run it; it is short.

set -eu

REPO="michele/quietbox"
# QUIETBOX_RELEASE_BASE exists for testing against a local directory.
RELEASE_BASE="${QUIETBOX_RELEASE_BASE:-https://github.com/$REPO/releases/download}"

say() { printf '%s\n' "$*"; }
fail() { printf 'install: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

main() {
  have curl || fail "curl is required"
  have tar || fail "tar is required"

  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported OS: $(uname -s) (quietbox supports Linux and macOS)" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac

  version="${VERSION:-}"
  if [ -z "$version" ]; then
    # /releases/latest redirects to /releases/tag/vX.Y.Z; a draft or missing
    # release does not, and then there is nothing to install.
    final=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
      fail "could not find the latest release; set VERSION=x.y.z"
    version="${final##*/v}"
    case "$version" in
      "" | *[!0-9.]*) fail "could not read a version from $final; set VERSION=x.y.z" ;;
    esac
  fi
  version="${version#v}"

  file="quietbox_${version}_${os}_${arch}.tar.gz"
  base="$RELEASE_BASE/v$version"
  dir="${INSTALL_DIR:-$HOME/.local/bin}"

  tmp=$(mktemp -d) || fail "could not create a temporary directory"
  trap 'rm -rf "$tmp"' EXIT INT TERM

  say "Downloading quietbox $version for $os/$arch"
  curl -fsSL -o "$tmp/$file" "$base/$file" || fail "download failed: $base/$file"
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"

  want=$(awk -v f="$file" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$want" ] || fail "$file is not listed in checksums.txt"
  if have sha256sum; then
    got=$(sha256sum "$tmp/$file" | awk '{ print $1 }')
  elif have shasum; then
    got=$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')
  else
    fail "sha256sum or shasum is required to verify the download"
  fi
  [ "$got" = "$want" ] || fail "checksum mismatch for $file; nothing was installed"

  tar -xzf "$tmp/$file" -C "$tmp" quietbox || fail "could not unpack $file"
  mkdir -p "$dir" || fail "could not create $dir"
  # Install through a temporary name so a running quietbox is replaced atomically.
  cp "$tmp/quietbox" "$dir/.quietbox.new" && chmod 755 "$dir/.quietbox.new" &&
    mv -f "$dir/.quietbox.new" "$dir/quietbox" || fail "could not write $dir/quietbox"

  say "Installed $dir/quietbox"
  case ":$PATH:" in
    *":$dir:"*) ;;
    *) say "Note: $dir is not on your PATH." ;;
  esac
  if [ "$os" = darwin ]; then
    say "macOS: the binary is not signed. If Gatekeeper blocks it, run:"
    say "  xattr -d com.apple.quarantine $dir/quietbox"
  fi
  say "Next: quietbox --version, then see https://quietbox.sh for the quick start."
}

main "$@"
