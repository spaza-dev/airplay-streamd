#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_SRC="${1:-$ROOT/streamd}"
INSTALL_BIN="/opt/streamd/streamd"

if [ "$(id -u)" -ne 0 ]; then
  echo "update.sh must be run as root (sudo $0)" >&2
  exit 1
fi

if [ ! -x "$BIN_SRC" ]; then
  echo "streamd binary not found or not executable: $BIN_SRC" >&2
  exit 1
fi

echo "==> Stopping streamd gracefully"
systemctl stop streamd

echo "==> Replacing executable (configuration, secrets, chunks and queue are preserved)"
install -m 0755 "$BIN_SRC" "$INSTALL_BIN"

echo "==> Reloading systemd and starting streamd"
systemctl daemon-reload
systemctl start streamd

sleep 2
if systemctl is-active --quiet streamd; then
  echo "==> streamd updated and running"
  systemctl --no-pager --full status streamd | sed -n '1,12p'
else
  echo "streamd failed to start after update; recent logs:" >&2
  journalctl -u streamd -n 60 --no-pager >&2 || true
  exit 1
fi
