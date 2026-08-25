#!/bin/sh
set -e

# shell-ai-server installer (Go binary)
# Usage: curl -fsSL https://raw.githubusercontent.com/qirong77/shell-ai-server/main/install.sh | sh
#   or:  sh install.sh [--port 9100] [--dir /opt/shell-ai-server]

DEFAULT_PORT=9100
INSTALL_DIR="/opt/shell-ai-server"
REPO_OWNER="qirong77"
REPO_NAME="shell-ai-server"
REPO_BRANCH="main"
PORT="$DEFAULT_PORT"

# ── Colors ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'
NC='\033[0m'

info()    { printf "${BLUE}[INFO]${NC}  %s\n" "$*"; }
success() { printf "${GREEN}[OK]${NC}    %s\n" "$*"; }
warn()    { printf "${YELLOW}[WARN]${NC}  %s\n" "$*"; }
fail()    { printf "${RED}[ERROR]${NC} %s\n" "$*"; exit 1; }

# ── Parse args ──────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --port)      PORT="$2"; shift 2 ;;
    --dir)       INSTALL_DIR="$2"; shift 2 ;;
    --branch)    REPO_BRANCH="$2"; shift 2 ;;
    *)           fail "Unknown option: $1" ;;
  esac
done

RAW_BASE="https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/${REPO_BRANCH}"
RELEASE_BASE="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download"

# ── Detect OS & Arch ────────────────────────────────────────────────────────
OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
  Darwin) OS_NAME="macos" ;;
  Linux)  OS_NAME="linux" ;;
  *)      fail "Unsupported OS: $OS (only macOS and Linux)" ;;
esac

case "$ARCH" in
  x86_64|amd64) ARCH_NAME="amd64" ;;
  arm64|aarch64) ARCH_NAME="arm64" ;;
  *) fail "Unsupported architecture: $ARCH" ;;
esac

BIN_NAME="shell-ai-server_${OS_NAME}_${ARCH_NAME}"
BIN_PATH="${INSTALL_DIR}/shell-ai-server"

info "Detected: ${OS_NAME}/${ARCH_NAME}"
info "Install dir: ${INSTALL_DIR}"

# ── Check if running as root ────────────────────────────────────────────────
IS_ROOT=false
if [ "$(id -u)" -eq 0 ]; then
  IS_ROOT=true
fi

# Non-root: fallback install dir to home
if [ "$IS_ROOT" = false ]; then
  case "$INSTALL_DIR" in
    /opt/*|/usr/*|/srv/*)
      INSTALL_DIR="${HOME}/shell-ai-server"
      warn "Non-root user, install dir changed to ${INSTALL_DIR}"
      ;;
  esac
  BIN_PATH="${INSTALL_DIR}/shell-ai-server"
fi

run_cmd() {
  if [ "$IS_ROOT" = true ]; then "$@"
  else sudo "$@"
  fi
}

# ── Ensure curl is installed ────────────────────────────────────────────────
if ! command -v curl >/dev/null 2>&1; then
  fail "curl is required. Please install curl and re-run this installer."
fi

mkdir -p "$INSTALL_DIR"

# ── Download precompiled binary ─────────────────────────────────────────────
info "Downloading ${BIN_NAME} from GitHub Releases..."
if curl -fsSL "${RELEASE_BASE}/${BIN_NAME}" -o "${BIN_PATH}.tmp"; then
  mv "${BIN_PATH}.tmp" "${BIN_PATH}"
  chmod +x "${BIN_PATH}"
  success "Downloaded binary (${BIN_NAME})"
else
  warn "Download failed — falling back to local build (requires Go)"
  if command -v go >/dev/null 2>&1; then
    ( cd "$(mktemp -d)" && \
      curl -fsSL "${RAW_BASE}/go.mod" -o go.mod && \
      for f in api.go disk.go docs.go files.go http.go main.go monitor.go network.go \
               proc_util.go process.go registry.go router.go services.go shell.go \
               shell_util.go system.go system_util.go tasks.go; do
        curl -fsSL "${RAW_BASE}/${f}" -o "$f"
      done && \
      GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" GOARCH="${ARCH_NAME}" \
      CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${BIN_PATH}" . ) || \
      fail "Build failed. Please install Go >= 1.22 or download a release binary manually."
    success "Built binary locally"
  else
    fail "No Go toolchain found and binary download failed. Please install Go >= 1.22 or download a release binary manually."
  fi
fi

# ── Linux: systemd service (if running as root) ─────────────────────────────
if [ "$OS" = "Linux" ] && [ "$IS_ROOT" = true ]; then
  info "Installing systemd service..."

  cat > /etc/systemd/system/shell-ai-server.service <<EOF
[Unit]
Description=Shell AI Server (Go)
After=network.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
Environment=PORT=${PORT}
ExecStart=${BIN_PATH}
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable shell-ai-server >/dev/null 2>&1 || true
  systemctl restart shell-ai-server

  sleep 1
  if systemctl is-active --quiet shell-ai-server; then
    success "systemd service shell-ai-server is active"
  else
    warn "systemd service failed to start — check: journalctl -u shell-ai-server"
  fi
else
  # macOS or non-root: run as a simple background process.
  info "Starting shell-ai-server on port ${PORT} (background)..."
  PORT="$PORT" nohup "${BIN_PATH}" > "${INSTALL_DIR}/shell-ai-server.log" 2>&1 &
  echo $! > "${INSTALL_DIR}/shell-ai-server.pid"
  sleep 1
  success "Started with pid $(cat "${INSTALL_DIR}/shell-ai-server.pid")"
fi

# ── Detect LAN IP ───────────────────────────────────────────────────────────
LAN_IP="localhost"
if [ "$OS" = "Darwin" ]; then
  LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || echo "localhost")
  [ -z "$LAN_IP" ] && LAN_IP=$(ipconfig getifaddr en1 2>/dev/null || echo "localhost")
else
  LAN_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
  [ -z "$LAN_IP" ] && LAN_IP="localhost"
fi

# ── Verify ──────────────────────────────────────────────────────────────────
if curl -sf "http://localhost:${PORT}/health" -o /dev/null 2>/dev/null; then
  success "Server is healthy on port ${PORT}"
else
  warn "Health check failed — check logs: ${INSTALL_DIR}/shell-ai-server.log"
fi

# ── Summary ─────────────────────────────────────────────────────────────────
echo ""
printf "${GREEN}════════════════════════════════════════════════════════════${NC}\n"
printf "${GREEN}  shell-ai-server is running!${NC}\n"
printf "${GREEN}════════════════════════════════════════════════════════════${NC}\n"
echo ""
echo "  Port:          ${PORT}"
echo "  Install dir:   ${INSTALL_DIR}"
echo "  Binary:        ${BIN_PATH}"
echo "  Go:            $(command -v go >/dev/null 2>&1 && go version || echo 'binary (no Go needed)')"
echo ""
echo "  Health check:  curl http://localhost:${PORT}/health"
echo "  API docs:      curl http://localhost:${PORT}/api"
echo "  LAN URL:       http://${LAN_IP}:${PORT}"
if [ "$OS" = "Linux" ] && [ "$IS_ROOT" = true ]; then
  echo ""
  echo "  Logs:          journalctl -u shell-ai-server -f"
  echo "  Restart:       systemctl restart shell-ai-server"
  echo "  Stop:          systemctl stop shell-ai-server"
  echo "  Status:        systemctl status shell-ai-server"
else
  echo ""
  echo "  Logs:          ${INSTALL_DIR}/shell-ai-server.log"
  echo "  Stop:          kill \$(cat ${INSTALL_DIR}/shell-ai-server.pid)"
fi
echo ""
