#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="/opt/indexer"
STATE_DIR="/var/lib/indexer"
SERVICE_NAME="indexer"
BUILD=0
NO_RESTART=0
BINARY=""
ORIGINAL_ARGS=("$@")

usage() {
  cat <<EOF
usage: sudo $0 [options]

Options:
  --build              Build the new binary and web assets first
  --binary PATH        Install a specific binary (default: dist/indexer)
  --prefix PATH        Install directory (default: $PREFIX)
  --state-dir PATH     State directory for backups (default: $STATE_DIR)
  --no-restart         Replace the binary but do not restart the service
  -h, --help           Show this help

The previous binary is backed up under \$STATE_DIR/backups and restored
automatically if the new service fails to start.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --build) BUILD=1; shift ;;
    --binary) BINARY="$2"; shift 2 ;;
    --prefix) PREFIX="$2"; shift 2 ;;
    --state-dir) STATE_DIR="$2"; shift 2 ;;
    --no-restart) NO_RESTART=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1; then
    exec sudo -E "$0" "${ORIGINAL_ARGS[@]}"
  fi
  echo "update.sh must run as root" >&2
  exit 1
fi

if [[ ! -x "$PREFIX/indexer" ]]; then
  echo "$PREFIX/indexer is not installed; use install.sh first" >&2
  exit 1
fi
if ! systemctl cat "${SERVICE_NAME}.service" >/dev/null 2>&1; then
  echo "systemd unit ${SERVICE_NAME}.service not found; use install.sh first" >&2
  exit 1
fi

if [[ "$BUILD" -eq 1 ]]; then
  "$ROOT/scripts/build.sh"
fi
if [[ -z "$BINARY" ]]; then
  BINARY="$ROOT/dist/indexer"
fi
if [[ ! -f "$BINARY" ]]; then
  echo "binary not found: $BINARY (run with --build or build first)" >&2
  exit 1
fi

"$BINARY" version

BACKUP_DIR="$STATE_DIR/backups"
install -d -m 0750 "$BACKUP_DIR"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP="$BACKUP_DIR/indexer-$TIMESTAMP"
cp -p "$PREFIX/indexer" "$BACKUP"
echo "==> Backed up current binary to $BACKUP"

echo "==> Installing $BINARY"
install -m 0755 -o root -g root "$BINARY" "$PREFIX/indexer.new"
mv -f "$PREFIX/indexer.new" "$PREFIX/indexer"

if [[ "$NO_RESTART" -eq 1 ]]; then
  echo "==> Binary replaced; service not restarted"
  exit 0
fi

echo "==> Restarting $SERVICE_NAME"
if systemctl restart "$SERVICE_NAME" && sleep 2 && systemctl is-active --quiet "$SERVICE_NAME"; then
  echo "==> Update complete"
  systemctl --no-pager --full status "$SERVICE_NAME" || true
  ls -1t "$BACKUP_DIR"/indexer-* 2>/dev/null | tail -n +6 | xargs -r rm -f
  exit 0
fi

echo "!! New binary failed to start; restoring $BACKUP" >&2
journalctl -u "$SERVICE_NAME" -n 40 --no-pager || true
install -m 0755 -o root -g root "$BACKUP" "$PREFIX/indexer.new"
mv -f "$PREFIX/indexer.new" "$PREFIX/indexer"
systemctl restart "$SERVICE_NAME" || true
echo "Rollback attempted. Inspect logs with: journalctl -u $SERVICE_NAME" >&2
exit 1
