#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO_VERSION="${GO_VERSION:-1.27.4}"
GO_BIN="${GO_BIN:-$HOME/.local/go/bin/go}"

if [ ! -x "$GO_BIN" ]; then
  echo "Go $GO_VERSION not found at $GO_BIN; downloading locally..."
  mkdir -p "$HOME/.local"
  archive="/tmp/go${GO_VERSION}.linux-amd64.tar.gz"
  curl -fsSL -o "$archive" "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  rm -rf "$HOME/.local/go"
  tar -C "$HOME/.local" -xzf "$archive"
fi

if [ -s "$HOME/.nvm/nvm.sh" ]; then
  # shellcheck disable=SC1091
  . "$HOME/.nvm/nvm.sh"
  nvm use 24 >/dev/null
else
  echo "nvm not found; assuming Node 24+ is already active" >&2
fi

if [ -f web/package-lock.json ]; then
  (cd web && npm ci)
else
  (cd web && npm install)
fi
(cd web && npm run build)

VERSION="${VERSION:-$(date -u +%Y%m%d%H%M%S)}"
echo "Building streamd ($VERSION)..."
CGO_ENABLED=0 "$GO_BIN" build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION}" \
  -o streamd .

echo "Built $ROOT/streamd"
