#!/usr/bin/env bash
# Build release binaries: scripts/release.sh VERSION [OUTDIR]
# Static (CGO off) binaries named consona_<version>_<os>_<arch>[.exe],
# plus SHA256SUMS and install.sh. Publishes nothing.
set -euo pipefail
VERSION=${1:?usage: scripts/release.sh VERSION [OUTDIR]}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${2:-$ROOT/dist}
rm -rf "$OUT" && mkdir -p "$OUT"
cd "$ROOT"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  os=${target%/*} arch=${target#*/} ext=
  [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$OUT/consona_${VERSION}_${os}_${arch}${ext}" ./cmd/consona
done
cp install.sh "$OUT/install.sh"
(cd "$OUT" && sha256sum consona_* install.sh > SHA256SUMS)
ls -l "$OUT"
