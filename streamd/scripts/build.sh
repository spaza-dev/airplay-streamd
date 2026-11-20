#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
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

NODE_MAJOR="${NODE_MAJOR:-24}"
NODE_MIN_VERSION="${NODE_MIN_VERSION:-20.19.0}"

version_ge() {
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

install_package() {
  local package="$1"
  if ! command -v apt-get >/dev/null 2>&1; then
    echo "missing dependency '$package'; install it and rerun" >&2
    exit 1
  fi
  if [ "$(id -u)" -eq 0 ]; then
    DEBIAN_FRONTEND=noninteractive apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$package"
  elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo env DEBIAN_FRONTEND=noninteractive apt-get update -y
    sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$package"
  else
    echo "missing dependency '$package'; run 'sudo apt-get install -y $package' and rerun" >&2
    exit 1
  fi
}

ensure_command() {
  local command_name="$1"
  local package="${2:-$1}"
  command -v "$command_name" >/dev/null 2>&1 && return 0
  echo "==> Installing missing build tool: $package"
  install_package "$package"
}

ensure_command curl curl
ensure_command tar tar
ensure_command sha256sum coreutils
ensure_command sort coreutils

if ! dpkg-query -W -f='${Status}' ca-certificates 2>/dev/null | grep -q '^install ok installed$' \
  && [ ! -f /etc/ssl/certs/ca-certificates.crt ]; then
  echo "==> Installing missing build tool: ca-certificates"
  install_package ca-certificates
fi

case "$(uname -m)" in
  x86_64|amd64)
    GO_ARCH="amd64"
    NODE_ARCH="x64"
    ;;
  aarch64|arm64)
    GO_ARCH="arm64"
    NODE_ARCH="arm64"
    ;;
  armv7l|armv7)
    GO_ARCH="armv6l"
    NODE_ARCH="armv7l"
    ;;
  i386|i686)
    GO_ARCH="386"
    NODE_ARCH="x86"
    ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

GO_REQUIRED="${GO_VERSION:-$(awk '/^go [0-9]/{print $2; exit}' "$ROOT/go.mod")}"

go_version() {
  "$1" version | awk '{print $3}' | sed 's/^go//'
}

find_go() {
  local candidate explicit version
  explicit="${GO:-${GO_BIN:-}}"
  if [ -n "$explicit" ]; then
    if [ ! -x "$explicit" ]; then
      echo "the configured Go executable does not exist: $explicit" >&2
      exit 1
    fi
    version="$(go_version "$explicit")"
    if ! version_ge "$version" "$GO_REQUIRED"; then
      echo "Go $version at $explicit is older than the required $GO_REQUIRED" >&2
      exit 1
    fi
    GO_BIN="$explicit"
    return 0
  fi

  for candidate in "$HOME/.local/go/bin/go" "$(command -v go 2>/dev/null || true)"; do
    [ -n "$candidate" ] && [ -x "$candidate" ] || continue
    version="$(go_version "$candidate")"
    if version_ge "$version" "$GO_REQUIRED"; then
      GO_BIN="$candidate"
      return 0
    fi
  done
  return 1
}

install_go() {
  local archive checksum go_json tmpdir version
  version="$GO_REQUIRED"
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
  if [ -z "$checksum" ]; then
    echo "could not determine the Go archive checksum" >&2
    exit 1
  fi

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT
  curl -fsSL --retry 3 "https://go.dev/dl/$archive" -o "$tmpdir/$archive"
  printf '%s  %s\n' "$checksum" "$tmpdir/$archive" | sha256sum -c - >/dev/null
  rm -rf "$HOME/.local/go"
  mkdir -p "$HOME/.local"
  tar -C "$HOME/.local" -xzf "$tmpdir/$archive"
  rm -rf "$tmpdir"
  trap - EXIT
  GO_BIN="$HOME/.local/go/bin/go"
}

node_ok() {
  command -v node >/dev/null 2>&1 \
    && command -v npm >/dev/null 2>&1 \
    && version_ge "$(node -p 'process.versions.node')" "$NODE_MIN_VERSION"
}

install_node_archive() {
  local archive base checksum install_root line sums tmpdir version bin_dir
  base="https://nodejs.org/dist/latest-v${NODE_MAJOR}.x"
  echo "==> Installing Node.js ${NODE_MAJOR}.x"
  sums="$(curl -fsSL --retry 3 "$base/SHASUMS256.txt")"
  line="$(printf '%s\n' "$sums" | awk -v p="linux-${NODE_ARCH}.tar.gz$" '$2 ~ p {print; exit}')"
  if [ -z "$line" ]; then
    echo "could not locate the Node.js archive for linux-${NODE_ARCH}" >&2
    exit 1
  fi
  checksum="$(printf '%s\n' "$line" | awk '{print $1}')"
  archive="$(printf '%s\n' "$line" | awk '{print $2}')"
  version="${archive#node-v}"
  version="${version%-linux-*}"

  if [ "$(id -u)" -eq 0 ]; then
    install_root="/usr/local/lib"
    bin_dir="/usr/local/bin"
  else
    install_root="$HOME/.local/lib"
    bin_dir="$HOME/.local/bin"
  fi
  mkdir -p "$install_root" "$bin_dir"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT
  curl -fsSL --retry 3 "$base/$archive" -o "$tmpdir/$archive"
  printf '%s  %s\n' "$checksum" "$tmpdir/$archive" | sha256sum -c - >/dev/null
  tar -C "$install_root" -xzf "$tmpdir/$archive"
  rm -rf "$tmpdir"
  trap - EXIT

  ln -sfn "$install_root/node-v${version}-linux-${NODE_ARCH}/bin/node" "$bin_dir/node"
  ln -sfn "$install_root/node-v${version}-linux-${NODE_ARCH}/bin/npm" "$bin_dir/npm"
  ln -sfn "$install_root/node-v${version}-linux-${NODE_ARCH}/bin/npx" "$bin_dir/npx"
  if [ -e "$install_root/node-v${version}-linux-${NODE_ARCH}/bin/corepack" ]; then
    ln -sfn "$install_root/node-v${version}-linux-${NODE_ARCH}/bin/corepack" "$bin_dir/corepack"
  fi
  export PATH="$bin_dir:$PATH"
}

install_node() {
  if [ -s "$HOME/.nvm/nvm.sh" ]; then
    set +u
    # shellcheck disable=SC1091
    . "$HOME/.nvm/nvm.sh"
    set -u
    if nvm which "$NODE_MAJOR" >/dev/null 2>&1; then
      nvm use "$NODE_MAJOR" >/dev/null
    else
      nvm install "$NODE_MAJOR"
      nvm use "$NODE_MAJOR" >/dev/null
    fi
    if node_ok; then
      echo "==> Node.js already installed via nvm: $(node -v)"
      return 0
    fi
  fi

  if node_ok; then
    echo "==> Node.js already installed: $(node -v)"
    return 0
  fi

  install_node_archive
  if ! node_ok; then
    echo "Node.js installation did not produce a usable node/npm" >&2
    exit 1
  fi
  echo "==> Node.js installed: $(node -v)"
}

if find_go; then
  echo "==> Using Go $(go_version "$GO_BIN") at $GO_BIN"
else
  install_go
fi

if [ "$SKIP_WEB" -eq 0 ]; then
  install_node
  echo "==> Building web frontend"
  (
    cd "$ROOT/web"
    if [ -f package-lock.json ]; then
      npm ci --no-audit --no-fund
    else
      npm install --no-audit --no-fund
    fi
    npm run build
  )
fi

if [ ! -f "$ROOT/web/dist/index.html" ]; then
  echo "web/dist/index.html is missing; run without --skip-web first" >&2
  exit 1
fi

VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || date -u +%Y%m%dT%H%M%SZ)}"
echo "==> Building streamd ($VERSION)"
TMP="$ROOT/.streamd.tmp.$$"
trap 'rm -f "$TMP"' EXIT
CGO_ENABLED=0 "$GO_BIN" build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION}" \
  -o "$TMP" .
mv -f "$TMP" "$ROOT/streamd"
chmod 0755 "$ROOT/streamd"
trap - EXIT

echo "==> Built $ROOT/streamd"
