#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_SRC="${1:-$ROOT/streamd}"
SERVICE_USER="streamd"
SERVICE_GROUP="streamd"
INSTALL_BIN="/opt/streamd/streamd"
CONFIG_DIR="/etc/streamd"
STATE_DIR="/var/lib/streamd"
LOG_DIR="/var/log/streamd"
UNIT_PATH="/etc/systemd/system/streamd.service"
SOCKET_UNIT_PATH="/etc/systemd/system/streamd.socket"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh must be run as root (sudo $0)" >&2
  exit 1
fi

if [ ! -x "$BIN_SRC" ]; then
  echo "streamd binary not found or not executable: $BIN_SRC" >&2
  echo "Build it first with scripts/build.sh, then rerun this installer." >&2
  exit 1
fi

echo "==> Installing system dependencies"
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends ffmpeg curl ca-certificates

if ! command -v yt-dlp >/dev/null 2>&1; then
  echo "==> Installing yt-dlp"
  curl -fsSL -o /usr/local/bin/yt-dlp \
    https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp
  chmod 0755 /usr/local/bin/yt-dlp
else
  echo "==> yt-dlp already installed: $(yt-dlp --version)"
fi

echo "==> Creating service user"
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$STATE_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi

echo "==> Creating directories"
install -d -m 0755 /opt/streamd
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_GROUP" \
  "$STATE_DIR" "$STATE_DIR/chunks" "$STATE_DIR/state" "$LOG_DIR"
install -d -m 0770 -o root -g "$SERVICE_GROUP" "$CONFIG_DIR"

echo "==> Installing executable"
install -m 0755 "$BIN_SRC" "$INSTALL_BIN"

if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
  echo "==> Installing default configuration"
  install -m 0660 -o root -g "$SERVICE_GROUP" "$ROOT/config.yaml" "$CONFIG_DIR/config.yaml"
else
  echo "==> Preserving existing configuration at $CONFIG_DIR/config.yaml"
fi

if [ ! -f "$CONFIG_DIR/.env" ]; then
  echo "==> Installing .env"
  if [ -f "$ROOT/.env" ]; then
    install -m 0640 -o root -g "$SERVICE_GROUP" "$ROOT/.env" "$CONFIG_DIR/.env"
  elif [ -f "$ROOT/../.env" ]; then
    install -m 0640 -o root -g "$SERVICE_GROUP" "$ROOT/../.env" "$CONFIG_DIR/.env"
  elif [ -f "$ROOT/.env.example" ]; then
    install -m 0640 -o root -g "$SERVICE_GROUP" "$ROOT/.env.example" "$CONFIG_DIR/.env"
    echo "    Edit $CONFIG_DIR/.env and add your S3/R2 credentials."
  else
    umask 027
    cat > "$CONFIG_DIR/.env" <<'EOF'
STREAMD_ADMIN_TOKEN=
S3_ENDPOINT=
S3_REGION=auto
S3_BUCKET=
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
EOF
    chown root:"$SERVICE_GROUP" "$CONFIG_DIR/.env"
  fi
else
  echo "==> Preserving existing secrets at $CONFIG_DIR/.env"
fi

chown root:"$SERVICE_GROUP" "$CONFIG_DIR" "$CONFIG_DIR/config.yaml" "$CONFIG_DIR/.env" 2>/dev/null || true
chmod 0770 "$CONFIG_DIR" && chgrp "$SERVICE_GROUP" "$CONFIG_DIR"
chmod 0660 "$CONFIG_DIR/config.yaml" 2>/dev/null || true
chmod 0640 "$CONFIG_DIR/.env"

echo "==> Installing systemd unit"
cat > "$UNIT_PATH" <<EOF
[Unit]
Description=streamd stream capture service
After=network-online.target streamd.socket
Wants=network-online.target
Requires=streamd.socket

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_GROUP
WorkingDirectory=$STATE_DIR
ExecStart=$INSTALL_BIN serve --config $CONFIG_DIR/config.yaml --env $CONFIG_DIR/.env
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
KillSignal=SIGTERM
LimitNOFILE=65536
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=$STATE_DIR $LOG_DIR
EnvironmentFile=-$CONFIG_DIR/.env

[Install]
WantedBy=multi-user.target
EOF

chmod 0644 "$UNIT_PATH"

cat > "$SOCKET_UNIT_PATH" <<EOF
[Unit]
Description=streamd control socket
PartOf=streamd.service

[Socket]
ListenStream=/run/streamd.sock
SocketMode=0660
SocketUser=$SERVICE_USER
SocketGroup=$SERVICE_GROUP
RemoveOnStop=true

[Install]
WantedBy=sockets.target
EOF

chmod 0644 "$SOCKET_UNIT_PATH"

echo "==> Enabling and starting streamd"
systemctl daemon-reload
systemctl enable streamd.socket streamd
systemctl start streamd.socket
systemctl restart streamd

sleep 2
if systemctl is-active --quiet streamd; then
  echo "==> streamd is running"
  systemctl --no-pager --full status streamd | sed -n '1,12p'
else
  echo "streamd failed to start; recent logs:" >&2
  journalctl -u streamd -n 60 --no-pager >&2 || true
  exit 1
fi
