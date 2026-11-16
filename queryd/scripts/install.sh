#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="/opt/queryd"
CONFIG_DIR="/etc/queryd"
STATE_DIR="/var/lib/queryd"
LOG_DIR="/var/log/queryd"
SERVICE_USER="queryd"
SERVICE_NAME="queryd"
BUILD=0
NO_START=0
BINARY=""
OLAF_VERSION="${OLAF_VERSION:-v3.3.3}"
OLAF_BINARY="${OLAF_BINARY:-}"
GO_BIN=""
ORIGINAL_ARGS=("$@")

usage() {
  cat <<EOF
usage: sudo $0 [options]

Options:
  --build              Build the frontend and backend before installing
  --binary PATH        Install a specific binary (default: dist/queryd or a fresh build)
  --olaf-version TAG   Olaf release to install when missing (default: $OLAF_VERSION)
  --olaf-binary PATH   Install a specific Olaf binary instead of downloading a release
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
    --olaf-version) OLAF_VERSION="$2"; shift 2 ;;
    --olaf-binary) OLAF_BINARY="$2"; shift 2 ;;
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

if ! command -v apt-get >/dev/null 2>&1 || ! command -v dpkg-query >/dev/null 2>&1; then
  echo "unsupported distribution: this installer requires apt and dpkg" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64)
    GO_ARCH="amd64"
    NODE_ARCH="x64"
    OLAF_ARCH="x86_64"
    ;;
  aarch64|arm64)
    GO_ARCH="arm64"
    NODE_ARCH="arm64"
    OLAF_ARCH="aarch64"
    ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

version_ge() {
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

install_packages() {
  local package
  local -a missing=()
  for package in "$@"; do
    if ! dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q '^install ok installed$'; then
      missing+=("$package")
    fi
  done
  if [[ "${#missing[@]}" -eq 0 ]]; then
    echo "==> System packages already installed"
    return
  fi
  echo "==> Installing system packages: ${missing[*]}"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y --no-install-recommends "${missing[@]}"
}

install_go() {
  local required version archive checksum go_json install_dir tmpdir
  required="$(awk '/^go [0-9]/{print $2; exit}' "$ROOT/go.mod")"

  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]]; then
    local user_home
    user_home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
    if [[ -x "$user_home/.local/go/bin/go" ]]; then
      GO_BIN="$user_home/.local/go/bin/go"
    fi
  fi

  if [[ -n "$GO_BIN" ]]; then
    version="$("$GO_BIN" version | awk '{print $3}' | sed 's/^go//')"
    if ! version_ge "$version" "$required"; then
      echo "Go $version is installed but $required or newer is required" >&2
      exit 1
    fi
    echo "==> Go already installed: $version"
    return
  fi

  version="$required"
  archive="go${version}.linux-${GO_ARCH}.tar.gz"
  echo "==> Installing Go $version"
  go_json="$(curl -fsSL --retry 3 https://go.dev/dl/?mode=json)"
  checksum="$(printf '%s\n' "$go_json" | awk -v f="$archive" '
    index($0, "\"filename\": \"" f "\"") { found=1; next }
    found && /"sha256"/ {
      sub(/.*"sha256": "/, "")
      sub(/".*/, "")
      print
      exit
    }
  ')"
  if [[ -z "$checksum" ]]; then
    echo "could not determine the Go archive checksum" >&2
    exit 1
  fi

  tmpdir="$(mktemp -d)"
  curl -fsSL --retry 3 "https://go.dev/dl/$archive" -o "$tmpdir/$archive"
  printf '%s  %s\n' "$checksum" "$tmpdir/$archive" | sha256sum -c - >/dev/null
  tar -C "$tmpdir" -xzf "$tmpdir/$archive"
  install_dir="/usr/local/lib/go-${version}"
  rm -rf "$install_dir"
  install -d "$(dirname "$install_dir")"
  mv "$tmpdir/go" "$install_dir"
  rm -rf "$tmpdir"
  ln -sfn "$install_dir/bin/go" /usr/local/bin/go
  ln -sfn "$install_dir/bin/gofmt" /usr/local/bin/gofmt
  GO_BIN="/usr/local/bin/go"
}

install_node() {
  local line version archive checksum base install_dir tmpdir node_version sums
  if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
    node_version="$(node -p 'process.versions.node')"
    if ! version_ge "$node_version" "20.19.0"; then
      echo "Node.js $node_version is installed but 20.19.0 or newer is required" >&2
      exit 1
    fi
    echo "==> Node.js already installed: $node_version"
    return
  fi

  base="https://nodejs.org/dist/latest-v24.x"
  sums="$(curl -fsSL --retry 3 "$base/SHASUMS256.txt")"
  line="$(printf '%s\n' "$sums" | awk -v p="linux-${NODE_ARCH}.tar.xz$" '$2 ~ p {print; exit}')"
  if [[ -z "$line" ]]; then
    echo "could not locate the Node.js archive" >&2
    exit 1
  fi
  checksum="$(printf '%s\n' "$line" | awk '{print $1}')"
  archive="$(printf '%s\n' "$line" | awk '{print $2}')"
  version="${archive#node-v}"
  version="${version%-linux-*}"

  echo "==> Installing Node.js $version"
  tmpdir="$(mktemp -d)"
  curl -fsSL --retry 3 "$base/$archive" -o "$tmpdir/$archive"
  printf '%s  %s\n' "$checksum" "$tmpdir/$archive" | sha256sum -c - >/dev/null
  install_dir="/usr/local/lib/node-v${version}-linux-${NODE_ARCH}"
  rm -rf "$install_dir"
  tar -C /usr/local/lib -xJf "$tmpdir/$archive"
  rm -rf "$tmpdir"
  ln -sfn "$install_dir/bin/node" /usr/local/bin/node
  ln -sfn "$install_dir/bin/npm" /usr/local/bin/npm
  ln -sfn "$install_dir/bin/npx" /usr/local/bin/npx
  ln -sfn "$install_dir/bin/corepack" /usr/local/bin/corepack
}

install_olaf() {
  local archive checksum url release_json asset info install_dir tmpdir version
  if command -v olaf >/dev/null 2>&1; then
    if [[ ! -e /usr/local/bin/olaf ]]; then
      ln -s "$(command -v olaf)" /usr/local/bin/olaf
    fi
    if ! olaf help >/dev/null 2>&1; then
      echo "the installed Olaf binary is not usable" >&2
      exit 1
    fi
    echo "==> Olaf already installed"
    return
  fi

  if [[ -n "$OLAF_BINARY" ]]; then
    if [[ ! -x "$OLAF_BINARY" ]]; then
      echo "Olaf binary not found or not executable: $OLAF_BINARY" >&2
      exit 1
    fi
    echo "==> Installing Olaf from $OLAF_BINARY"
    install -m 0755 -o root -g root "$OLAF_BINARY" /usr/local/bin/olaf
    return
  fi

  version="${OLAF_VERSION#v}"
  archive="olaf_cli-linux-${OLAF_ARCH}.tar.gz"
  echo "==> Installing Olaf v$version"
  release_json="$(curl -fsSL --retry 3 "https://api.github.com/repos/JorenSix/Olaf/releases/tags/v${version}")"
  info="$(printf '%s\n' "$release_json" | awk -v n="$archive" '
    index($0, "\"name\": \"" n "\"") { found=1 }
    found && /"digest": "sha256:/ {
      d=$0
      sub(/.*sha256:/, "", d)
      sub(/".*/, "", d)
    }
    found && /"browser_download_url":/ {
      u=$0
      sub(/.*"browser_download_url": "/, "", u)
      sub(/".*/, "", u)
      print d
      print u
      exit
    }
  ')"
  checksum="$(printf '%s\n' "$info" | sed -n '1p')"
  url="$(printf '%s\n' "$info" | sed -n '2p')"
  if [[ -z "$checksum" || -z "$url" ]]; then
    echo "could not resolve the Olaf v$version release asset" >&2
    echo "use --olaf-binary PATH to install a local Olaf binary" >&2
    exit 1
  fi

  tmpdir="$(mktemp -d)"
  curl -fsSL --retry 3 "$url" -o "$tmpdir/$archive"
  printf '%s  %s\n' "$checksum" "$tmpdir/$archive" | sha256sum -c - >/dev/null
  tar -C "$tmpdir" -xzf "$tmpdir/$archive"
  if [[ ! -x "$tmpdir/olaf" ]]; then
    echo "the Olaf release archive did not contain the olaf binary" >&2
    exit 1
  fi
  install -m 0755 -o root -g root "$tmpdir/olaf" /usr/local/bin/olaf
  if [[ -f "$tmpdir/olaf_config.example.json" ]]; then
    install -d -m 0755 /usr/local/share/olaf
    install -m 0644 -o root -g root \
      "$tmpdir/olaf_config.example.json" /usr/local/share/olaf/olaf_config.example.json
  fi
  if [[ -f "$tmpdir/olaf_config.schema.json" ]]; then
    install -d -m 0755 /usr/local/share/olaf
    install -m 0644 -o root -g root \
      "$tmpdir/olaf_config.schema.json" /usr/local/share/olaf/olaf_config.schema.json
  fi
  rm -rf "$tmpdir"
}

run_build() {
  echo "==> Building queryd frontend and backend"
  local -a build_env=("PATH=$PATH" "GO=$GO_BIN")
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]] \
    && sudo -H -u "$SUDO_USER" test -w "$ROOT"; then
    sudo -H -u "$SUDO_USER" env "${build_env[@]}" "$ROOT/scripts/build.sh"
  else
    env "${build_env[@]}" "$ROOT/scripts/build.sh"
  fi
}

echo "==> Installing system dependencies"
install_packages ffmpeg ca-certificates tzdata curl tar xz-utils

echo "==> Ensuring build environment"
install_go
install_node

echo "==> Ensuring Olaf indexer"
install_olaf

if ! command -v ffprobe >/dev/null 2>&1; then
  echo "ffprobe is not available after dependency installation" >&2
  exit 1
fi
if ! command -v olaf >/dev/null 2>&1; then
  echo "olaf is not available after dependency installation" >&2
  exit 1
fi

if [[ "$BUILD" -eq 1 ]]; then
  run_build
elif [[ -z "$BINARY" && ! -f "$ROOT/dist/queryd" ]]; then
  run_build
fi

if [[ -z "$BINARY" ]]; then
  BINARY="$ROOT/dist/queryd"
fi
if [[ ! -f "$BINARY" ]]; then
  echo "binary not found: $BINARY" >&2
  exit 1
fi

echo "==> Creating service user and directories"
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home "$STATE_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
install -d -m 0755 -o root -g root "$PREFIX"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$STATE_DIR" "$STATE_DIR/jobs" "$LOG_DIR"
install -d -m 0750 -o root -g "$SERVICE_USER" "$CONFIG_DIR"

echo "==> Installing binary"
install -m 0755 -o root -g root "$BINARY" "$PREFIX/queryd.new"
mv -f "$PREFIX/queryd.new" "$PREFIX/queryd"

if [[ ! -f "$CONFIG_DIR/config.yaml" ]]; then
  echo "==> Installing default configuration"
  sed \
    -e "s#/var/lib/queryd#${STATE_DIR}#g" \
    -e "s#/var/log/queryd#${LOG_DIR}#g" \
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
  "$ROOT/scripts/queryd.service" > "/etc/systemd/system/${SERVICE_NAME}.service"
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
  binary:  $PREFIX/queryd
  config:  $CONFIG_DIR/config.yaml
  env:     $CONFIG_DIR/.env
  state:   $STATE_DIR
  service: $SERVICE_NAME
  olaf:    $(command -v olaf)
  ffprobe: $(command -v ffprobe || echo available)
  go:      $GO_BIN
  node:    $(command -v node)

Next steps:
  1. Edit $CONFIG_DIR/.env (admin token, optional callback secret, S3 credentials).
  2. Edit $CONFIG_DIR/config.yaml if paths or worker limits differ.
  3. Open http://127.0.0.1:8790/ and configure S3 credentials in the environment file.
EOF
