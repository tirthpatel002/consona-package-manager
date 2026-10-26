#!/bin/sh
# Install consona: downloads the binary for this OS/arch from a release
# directory, checks it against SHA256SUMS, and installs it.
#
#   curl -fsSL <base-url>/install.sh | sh
#   curl -fsSL <base-url>/install.sh | sh -s -- --base-url URL [--system] [--dir DIR]
#
# --base-url URL  release directory (or CONSONA_BASE_URL)
# --dir DIR       install directory (default ~/.local/bin)
# --system        install to /usr/local/bin with sudo
set -eu

BASE_URL=${CONSONA_BASE_URL:-https://REPLACE-WITH-RELEASE-URL}
DIR=${CONSONA_INSTALL_DIR:-$HOME/.local/bin}
SUDO=
while [ $# -gt 0 ]; do
  case $1 in
    --base-url) BASE_URL=$2; shift 2 ;;
    --base-url=*) BASE_URL=${1#*=}; shift ;;
    --dir) DIR=$2; shift 2 ;;
    --dir=*) DIR=${1#*=}; shift ;;
    --system) DIR=/usr/local/bin; SUDO=sudo; shift ;;
    *) echo "consona install: unknown option $1" >&2; exit 2 ;;
  esac
done
BASE_URL=${BASE_URL%/}
case $BASE_URL in *REPLACE-WITH-RELEASE-URL*)
  echo "consona install: no release URL yet; pass --base-url URL or set CONSONA_BASE_URL" >&2; exit 2 ;;
esac

die() { echo "consona install: $*" >&2; exit 1; }
case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s) (on Windows, download consona_<version>_windows_amd64.exe from $BASE_URL)" ;;
esac
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then fetch() { wget -q "$1" -O "$2"; }
else die "need curl or wget"; fi
if command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else die "need sha256sum or shasum"; fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch "$BASE_URL/SHA256SUMS" "$tmp/SHA256SUMS" || die "cannot download $BASE_URL/SHA256SUMS"
line=$(grep -E " consona_[^ ]+_${os}_${arch}\$" "$tmp/SHA256SUMS" | head -n 1) || true
[ -n "$line" ] || die "no consona build for ${os}/${arch} in $BASE_URL"
want=${line%% *}
file=${line##* }
fetch "$BASE_URL/$file" "$tmp/consona" || die "cannot download $BASE_URL/$file"
got=$(sha "$tmp/consona")
[ "$got" = "$want" ] || die "checksum mismatch for $file (expected $want, got $got); nothing installed"
chmod 755 "$tmp/consona"

$SUDO mkdir -p "$DIR"
$SUDO mv "$tmp/consona" "$DIR/consona"
echo "consona: installed $file to $DIR/consona"
case ":$PATH:" in *":$DIR:"*) ;; *)
  echo "consona: $DIR is not on your PATH; add it, e.g.: echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" ;;
esac
echo
echo "Next steps:"
if ! command -v gh >/dev/null 2>&1; then
  echo "  1. install the GitHub CLI (https://cli.github.com) and run: gh auth login"
elif ! gh auth status >/dev/null 2>&1; then
  echo "  1. log in to GitHub: gh auth login"
else
  echo "  1. GitHub login: ok (gh)"
fi
echo "  2. point npm at consona (once): consona setup"
echo "  then use npm as usual; undo any time with: consona setup --undo"
