#!/bin/sh
set -e

# shell-ai-server installer
# Usage: curl -fsSL https://raw.githubusercontent.com/qirong77/shell-ai-server/main/install.sh | sh
#   or:  sh install.sh [--port 9100] [--dir /opt/shell-ai-server]

DEFAULT_PORT=9100
INSTALL_DIR="/opt/shell-ai-server"
REPO_RAW="https://raw.githubusercontent.com"
REPO_OWNER="qirong77"
REPO_NAME="shell-ai-server"
REPO_BRANCH="main"
PORT="$DEFAULT_PORT"
NODE_MIN_MAJOR=18

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
    --repo)      REPO_OWNER="$2"; shift 2 ;;
    --branch)    REPO_BRANCH="$2"; shift 2 ;;
    --name)      REPO_NAME="$2"; shift 2 ;;
    *)           fail "Unknown option: $1" ;;
  esac
done

RAW_BASE="${REPO_RAW}/${REPO_OWNER}/${REPO_NAME}/${REPO_BRANCH}"

# ── Detect OS & Arch ────────────────────────────────────────────────────────
OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
  Darwin) OS_NAME="macOS" ;;
  Linux)  OS_NAME="Linux" ;;
  *)      fail "Unsupported OS: $OS (only macOS and Linux)" ;;
esac

case "$ARCH" in
  x86_64|amd64) ARCH_NAME="x64" ;;
  arm64|aarch64) ARCH_NAME="arm64" ;;
  *) fail "Unsupported architecture: $ARCH" ;;
esac

# ── Detect Linux distro ─────────────────────────────────────────────────────
DISTRO="unknown"
PKG_MANAGER=""

if [ "$OS" = "Linux" ]; then
  if [ -f /etc/os-release ]; then
    . /etc/os-release
    DISTRO="$ID"
  elif [ -f /etc/redhat-release ]; then
    DISTRO="rhel"
  fi

  case "$DISTRO" in
    alpine)               PKG_MANAGER="apk"   ;;
    debian|ubuntu|linuxmint) PKG_MANAGER="apt"  ;;
    centos|rhel|fedora|rocky|almalinux|amzn)
      if command -v dnf >/dev/null 2>&1; then PKG_MANAGER="dnf"
      else PKG_MANAGER="yum"; fi ;;
    arch|manjaro)         PKG_MANAGER="pacman" ;;
    *)                    PKG_MANAGER="" ;;
  esac
fi

info "Detected: ${OS_NAME} (${ARCH_NAME}) ${DISTRO:+— $DISTRO}"

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
fi

# ── Helper: run a command with or without sudo ──────────────────────────────
run_cmd() {
  if [ "$IS_ROOT" = true ]; then "$@"
  else sudo "$@"
  fi
}

# ── Ensure curl is installed ────────────────────────────────────────────────
if ! command -v curl >/dev/null 2>&1; then
  info "curl not found, installing..."
  if [ -z "$PKG_MANAGER" ]; then
    fail "curl is not installed and no supported package manager found. Please install curl manually."
  fi
  case "$PKG_MANAGER" in
    apk)    run_cmd apk add --no-cache curl ;;
    apt)    run_cmd sh -c 'apt-get update -qq && apt-get install -y -qq curl' ;;
    dnf)    run_cmd dnf install -y -q curl ;;
    yum)    run_cmd yum install -y -q curl ;;
    pacman) run_cmd pacman -S --noconfirm curl ;;
  esac
  success "curl installed"
fi

# ── Check existing Node.js ──────────────────────────────────────────────────
need_install_node=false

if command -v node >/dev/null 2>&1; then
  NODE_VERSION="$(node -v | sed 's/v//')"
  NODE_MAJOR="${NODE_VERSION%%.*}"
  if [ "$NODE_MAJOR" -ge "$NODE_MIN_MAJOR" ]; then
    success "Node.js v${NODE_VERSION} found (>= ${NODE_MIN_MAJOR})"
  else
    warn "Node.js v${NODE_VERSION} found but need >= ${NODE_MIN_MAJOR}, will upgrade"
    need_install_node=true
  fi
else
  warn "Node.js not found"
  need_install_node=true
fi

# ── Install Node.js ─────────────────────────────────────────────────────────
if [ "$need_install_node" = true ]; then

  # Alpine: must use apk (nvm/official binaries don't work with musl)
  if [ "$DISTRO" = "alpine" ]; then
    info "Installing Node.js via apk (Alpine)..."
    run_cmd apk add --no-cache nodejs npm

  # macOS: use Homebrew or nvm
  elif [ "$OS" = "Darwin" ]; then
    if command -v brew >/dev/null 2>&1; then
      info "Installing Node.js via Homebrew..."
      brew install node
    else
      info "Installing Node.js v${NODE_MIN_MAJOR} via nvm..."
      NVM_DIR="${HOME}/.nvm"
      if [ ! -d "$NVM_DIR" ]; then
        curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | sh
      fi
      [ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
      nvm install "$NODE_MIN_MAJOR" --lts
      nvm use "$NODE_MIN_MAJOR" --lts
      nvm alias default "$NODE_MIN_MAJOR" --lts
    fi

  # Linux (non-Alpine): try nvm first, fall back to distro package manager
  else
    info "Installing Node.js v${NODE_MIN_MAJOR} via nvm..."

    NVM_DIR="${HOME}/.nvm"
    if [ ! -d "$NVM_DIR" ]; then
      curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | sh
    fi

    [ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"

    nvm install "$NODE_MIN_MAJOR" --lts 2>/dev/null || {
      warn "nvm install failed, trying package manager..."
      case "$PKG_MANAGER" in
        apt)
          run_cmd sh -c 'apt-get update -qq && apt-get install -y -qq nodejs npm'
          ;;
        dnf)
          run_cmd dnf install -y -q nodejs
          ;;
        yum)
          run_cmd sh -c "curl -fsSL https://rpm.nodesource.com/setup_${NODE_MIN_MAJOR}.x | bash - && yum install -y -q nodejs"
          ;;
        pacman)
          run_cmd pacman -S --noconfirm nodejs npm
          ;;
        *)
          fail "Could not install Node.js. Please install Node.js >= ${NODE_MIN_MAJOR} manually."
          ;;
      esac
    }

    # Re-source nvm for PATH
    [ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
  fi

  # Verify node is now available
  if ! command -v node >/dev/null 2>&1; then
    [ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh" && nvm use default 2>/dev/null || true
    [ -x "/usr/local/bin/node" ] && export PATH="/usr/local/bin:$PATH" || true
    [ -x "/usr/bin/node" ] && export PATH="/usr/bin:$PATH" || true
  fi

  command -v node >/dev/null 2>&1 || fail "Node.js installation failed. Please install Node.js >= ${NODE_MIN_MAJOR} manually."
  success "Node.js $(node -v) installed"
fi

# ── Install pm2 ─────────────────────────────────────────────────────────────
if ! command -v pm2 >/dev/null 2>&1; then
  info "Installing pm2 globally..."

  # Non-root without sudo: use npm prefix to home dir
  if [ "$IS_ROOT" = false ] && ! command -v sudo >/dev/null 2>&1; then
    warn "No root access — configuring npm prefix to ${HOME}/.npm-global"
    mkdir -p "${HOME}/.npm-global"
    npm config set prefix "${HOME}/.npm-global"
    export PATH="${HOME}/.npm-global/bin:$PATH"
    echo "export PATH=\"${HOME}/.npm-global/bin:\$PATH\"" >> "${HOME}/.bashrc" 2>/dev/null || true
  fi

  npm install -g pm2
  success "pm2 $(pm2 --version) installed"
else
  success "pm2 $(pm2 --version) found"
fi

# ── Download shell-server.mjs ───────────────────────────────────────────────
info "Downloading shell-server.mjs from GitHub..."

mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"

if curl -fsSL "${RAW_BASE}/shell-server.mjs" -o shell-server.mjs; then
  success "Downloaded shell-server.mjs"
else
  fail "Failed to download from ${RAW_BASE}/shell-server.mjs — check repo/branch settings"
fi

# ── Stop existing pm2 process if any ────────────────────────────────────────
if pm2 describe shell-ai-server >/dev/null 2>&1; then
  info "Stopping existing shell-ai-server process..."
  pm2 delete shell-ai-server
fi

# ── Start with pm2 ──────────────────────────────────────────────────────────
info "Starting shell-ai-server on port ${PORT}..."

export PORT="$PORT"
pm2 start shell-server.mjs \
  --name shell-ai-server \
  --update-env \
  --time

pm2 save

# ── Wait for startup ────────────────────────────────────────────────────────
sleep 2

# ── Detect LAN IP ───────────────────────────────────────────────────────────
LAN_IP="localhost"

if [ "$OS" = "Darwin" ]; then
  LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || echo "localhost")
  [ -z "$LAN_IP" ] && LAN_IP=$(ipconfig getifaddr en1 2>/dev/null || echo "localhost")
else
  LAN_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
  [ -z "$LAN_IP" ] && LAN_IP=$(ip -4 addr show scope global 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | head -1 || true)
  [ -z "$LAN_IP" ] && LAN_IP=$(ip addr show 2>/dev/null | grep 'inet ' | grep -v '127.0.0.1' | awk '{print $2}' | cut -d/ -f1 | head -1 || true)
  [ -z "$LAN_IP" ] && LAN_IP="localhost"
fi

# ── Verify ──────────────────────────────────────────────────────────────────
if curl -sf "http://localhost:${PORT}/health" -o /dev/null 2>/dev/null; then
  success "Server is healthy on port ${PORT}"
else
  warn "Health check failed — check logs: pm2 logs shell-ai-server"
fi

# ── Setup pm2 startup (auto-restart on boot) ────────────────────────────────
info "Configuring pm2 auto-startup on boot..."
STARTUP_OUTPUT=$(pm2 startup 2>&1 || true)

if echo "$STARTUP_OUTPUT" | grep -q 'sudo'; then
  warn "Run this command to enable auto-startup on boot:"
  echo ""
  echo "$STARTUP_OUTPUT" | grep 'sudo' | head -1
  echo ""
  echo "  pm2 save"
else
  success "pm2 startup configured"
fi

# ── Summary ─────────────────────────────────────────────────────────────────
echo ""
printf "${GREEN}════════════════════════════════════════════════════════════${NC}\n"
printf "${GREEN}  shell-ai-server is running!${NC}\n"
printf "${GREEN}════════════════════════════════════════════════════════════${NC}\n"
echo ""
echo "  Port:          ${PORT}"
echo "  Install dir:   ${INSTALL_DIR}"
echo "  Node.js:       $(node -v)"
echo "  pm2:           $(pm2 --version)"
echo ""
echo "  Health check:  curl http://localhost:${PORT}/health"
echo "  API docs:      curl http://localhost:${PORT}/api"
echo "  LAN URL:       http://${LAN_IP}:${PORT}"
echo ""
echo "  Logs:          pm2 logs shell-ai-server"
echo "  Restart:       pm2 restart shell-ai-server"
echo "  Stop:          pm2 stop shell-ai-server"
echo "  Status:        pm2 status"
echo ""
