#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="/opt/indexer"
CONFIG_DIR="/etc/indexer"
STATE_DIR="/var/lib/indexer"
LOG_DIR="/var/log/indexer"
SERVICE_USER="indexer"
SERVICE_NAME="indexer"
BUILD=0
NO_START=0
BINARY=""
ORIGINAL_ARGS=("$@")

usage() {
  cat <<EOF
usage: sudo $0 [options]

Options:
  --build              Build the binary and web assets before installing
  --binary PATH        Install a specific binary (default: dist/indexer)
  --prefix PATH        Install directory (default: $PREFIX)
  --config-dir PATH    Configuration directory (default: $CONFIG_DIR)
  --state-dir PATH     State/database directory (default: $STATE_DIR)
  --log-dir PATH       Log directory (default: $LOG_DIR)
  --user NAME          Service user (default: $SERVICE_USER)
  --no-start           Install and enable but do not start
  -h, --help           Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --build) BUILD=1; shift ;;
    --binary) BINARY="$2"; shift 2 ;;
    --prefix) PREFIX="$2"; shift 2 ;;
    --config-dir) CONFIG_DIR="$2"; shift 2 ;;
    --state-dir) STATE_DIR="$2"; shift 2 ;;
    --log-dir) LOG_DIR="$2"; shift 2 ;;
    --user) SERVICE_USER="$2"; shift 2 ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1; then
    exec sudo -E "$0" "${ORIGINAL_ARGS[@]}"
  fi
  echo "install.sh must run as root" >&2
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

for tool in olaf ffprobe; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "WARNING: $tool is not on PATH; the service will fail readiness checks" >&2
  fi
done

echo "==> Creating service user and directories"
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home "$STATE_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
install -d -m 0755 -o root -g root "$PREFIX"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$STATE_DIR" "$STATE_DIR/jobs" "$LOG_DIR"
install -d -m 0750 -o root -g "$SERVICE_USER" "$CONFIG_DIR"

echo "==> Installing binary"
install -m 0755 -o root -g root "$BINARY" "$PREFIX/indexer.new"
mv -f "$PREFIX/indexer.new" "$PREFIX/indexer"

if [[ ! -f "$CONFIG_DIR/config.yaml" ]]; then
  echo "==> Installing default configuration"
  sed \
    -e "s#/var/lib/indexer#${STATE_DIR}#g" \
    -e "s#/var/log/indexer#${LOG_DIR}#g" \
    "$ROOT/config.yaml" > "$CONFIG_DIR/config.yaml"
  chown root:"$SERVICE_USER" "$CONFIG_DIR/config.yaml"
  chmod 0640 "$CONFIG_DIR/config.yaml"
else
  echo "==> Keeping existing $CONFIG_DIR/config.yaml"
fi

if [[ ! -f "$CONFIG_DIR/.env" ]]; then
  echo "==> Installing environment template"
  install -m 0640 -o root -g "$SERVICE_USER" "$ROOT/.env.example" "$CONFIG_DIR/.env"
else
  echo "==> Keeping existing $CONFIG_DIR/.env"
fi

echo "==> Installing systemd unit"
sed \
  -e "s#@PREFIX@#${PREFIX}#g" \
  -e "s#@CONFIGDIR@#${CONFIG_DIR}#g" \
  -e "s#@STATE@#${STATE_DIR}#g" \
  -e "s#@LOGDIR@#${LOG_DIR}#g" \
  -e "s#@USER@#${SERVICE_USER}#g" \
  "$ROOT/scripts/indexer.service" > "/etc/systemd/system/${SERVICE_NAME}.service"
chmod 0644 "/etc/systemd/system/${SERVICE_NAME}.service"

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

if [[ "$NO_START" -eq 0 ]]; then
  systemctl restart "$SERVICE_NAME"
  echo "==> Service status"
  systemctl --no-pager --full status "$SERVICE_NAME" || true
else
  echo "==> Installed and enabled (not started)"
fi

cat <<EOF

Installed:
  binary:  $PREFIX/indexer
  config:  $CONFIG_DIR/config.yaml
  env:     $CONFIG_DIR/.env
  state:   $STATE_DIR
  service: $SERVICE_NAME

Next steps:
  1. Edit $CONFIG_DIR/.env (admin/event tokens, optional S3 defaults).
  2. Edit $CONFIG_DIR/config.yaml if paths or worker limits differ.
  3. Configure storage and discovery at http://127.0.0.1:8788/
EOF
