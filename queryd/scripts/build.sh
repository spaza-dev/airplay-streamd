#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SKIP_WEB=0
for arg in "$@"; do
  case "$arg" in
    --skip-web) SKIP_WEB=1 ;;
    -h|--help)
      echo "usage: $0 [--skip-web]"
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

if [[ -n "${GO:-}" ]]; then
  GO_BIN="$GO"
elif [[ -x "$HOME/.local/go/bin/go" ]]; then
  GO_BIN="$HOME/.local/go/bin/go"
elif command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
else
  echo "Go toolchain not found; set GO=/path/to/go" >&2
  exit 1
fi

if [[ "$SKIP_WEB" -eq 0 ]]; then
  if ! command -v npm >/dev/null 2>&1; then
    if [[ -s "$HOME/.nvm/nvm.sh" ]]; then
      # shellcheck disable=SC1091
      source "$HOME/.nvm/nvm.sh"
      nvm use 24 >/dev/null 2>&1 || true
    fi
  fi
  if ! command -v npm >/dev/null 2>&1; then
    echo "npm not found; install Node 24 or pass --skip-web with a prebuilt web/dist" >&2
    exit 1
  fi
  echo "==> Building web frontend"
  pushd "$ROOT/web" >/dev/null
  if [[ -f package-lock.json ]]; then
    npm ci --no-audit --no-fund
  else
    npm install --no-audit --no-fund
  fi
  npm run build
  popd >/dev/null
fi

if [[ ! -f "$ROOT/web/dist/index.html" ]]; then
  echo "web/dist/index.html is missing; run without --skip-web first" >&2
  exit 1
fi

VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || date -u +%Y%m%dT%H%M%SZ)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

mkdir -p "$ROOT/dist"
TMP="$ROOT/dist/.queryd.tmp.$$"
trap 'rm -f "$TMP"' EXIT

echo "==> Building queryd ${VERSION}"
pushd "$ROOT" >/dev/null
CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags "$LDFLAGS" -o "$TMP" .
mv -f "$TMP" "$ROOT/dist/queryd"
chmod 0755 "$ROOT/dist/queryd"
popd >/dev/null
trap - EXIT

echo "==> Built $ROOT/dist/queryd"
"$ROOT/dist/queryd" version
