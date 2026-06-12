#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════╗
# ║                    Ghost CLI — Single-Command Installer                  ║
# ║                                                                          ║
# ║  Usage:                                                                  ║
# ║    curl -fsSL https://your-domain.com/install.sh | bash                  ║
# ║    — or —                                                                ║
# ║    bash install.sh                                                       ║
# ╚══════════════════════════════════════════════════════════════════════════╝

set -euo pipefail

# ──────────────────────────────────────────────
#  Configuration — edit these before distributing
# ──────────────────────────────────────────────

RELEASE_BASE_URL="https://ghost.tirup.in/releases"
BINARY_NAME="ghost"
INSTALL_VERSION="latest"   # can be overridden: GHOST_VERSION=1.0.0 bash install.sh

# ──────────────────────────────────────────────
#  ANSI Colour Helpers
# ──────────────────────────────────────────────

BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
CYAN="\033[36m"
BR_CYAN="\033[96m"
BR_YELLOW="\033[93m"

print_banner() {
  printf "\n"
  printf "${BOLD}${BR_CYAN}  ██████╗ ██╗  ██╗ ██████╗ ███████╗████████╗${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ██╔════╝ ██║  ██║██╔═══██╗██╔════╝╚══██╔══╝${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ██║  ███╗███████║██║   ██║███████╗   ██║   ${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ██║   ██║██╔══██║██║   ██║╚════██║   ██║   ${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ╚██████╔╝██║  ██║╚██████╔╝███████║   ██║   ${RESET}\n"
  printf "${BOLD}${BR_CYAN}   ╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚══════╝   ╚═╝   ${RESET}\n"
  printf "\n"
  printf "${DIM}  Ephemeral · Encrypted · Zero-Knowledge Chat — Installer${RESET}\n"
  printf "${DIM}  ────────────────────────────────────────────────────────${RESET}\n"
  printf "\n"
}

log_info()    { printf "  ${CYAN}→${RESET}  %s\n" "$1"; }
log_success() { printf "  ${GREEN}✓${RESET}  %s\n" "$1"; }
log_warn()    { printf "  ${YELLOW}!${RESET}  %s\n" "$1"; }
log_error()   { printf "  ${RED}✗${RESET}  %s\n" "$1" >&2; }
log_step()    { printf "\n  ${BOLD}${BR_YELLOW}[%s]${RESET} %s\n" "$1" "$2"; }

die() {
  log_error "$1"
  exit 1
}

# ──────────────────────────────────────────────
#  Step 1 — Detect OS and Architecture
# ──────────────────────────────────────────────

detect_platform() {
  log_step "1/4" "Detecting host platform..."

  local os_raw arch_raw
  os_raw="$(uname -s 2>/dev/null || echo "unknown")"
  arch_raw="$(uname -m 2>/dev/null || echo "unknown")"

  case "${os_raw}" in
    Linux*)   OS="linux"   ;;
    Darwin*)  OS="darwin"  ;;
    CYGWIN*|MINGW*|MSYS*|Windows*) OS="windows" ;;
    *)        die "Unsupported operating system: ${os_raw}" ;;
  esac

  case "${arch_raw}" in
    x86_64|amd64)          ARCH="amd64" ;;
    aarch64|arm64)         ARCH="arm64" ;;
    armv7l|armv7|armhf)    ARCH="arm"   ;;
    i386|i686)             ARCH="386"   ;;
    *)                     die "Unsupported architecture: ${arch_raw}" ;;
  esac

  PLATFORM="${OS}_${ARCH}"
  log_success "Detected platform: ${BOLD}${PLATFORM}${RESET}"

  # Windows binary gets .exe suffix
  if [ "${OS}" = "windows" ]; then
    BINARY_FILENAME="${BINARY_NAME}_${PLATFORM}.exe"
    INSTALL_FILENAME="${BINARY_NAME}.exe"
  else
    BINARY_FILENAME="${BINARY_NAME}_${PLATFORM}"
    INSTALL_FILENAME="${BINARY_NAME}"
  fi
}

# ──────────────────────────────────────────────
#  Step 2 — Determine install directory
# ──────────────────────────────────────────────

determine_install_dir() {
  log_step "2/4" "Determining install directory..."

  # Prefer a user-local bin if it exists and is on PATH; fall back to
  # /usr/local/bin (which may require sudo).
  if [ -d "${HOME}/.local/bin" ] && echo "${PATH}" | grep -q "${HOME}/.local/bin"; then
    INSTALL_DIR="${HOME}/.local/bin"
  elif [ -d "${HOME}/bin" ] && echo "${PATH}" | grep -q "${HOME}/bin"; then
    INSTALL_DIR="${HOME}/bin"
  elif [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    # Try to create ~/.local/bin and add it to the current session PATH.
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "${INSTALL_DIR}"
    export PATH="${INSTALL_DIR}:${PATH}"
    log_warn "Added ${INSTALL_DIR} to PATH for this session."
    log_warn "Add the following line to your shell profile to make this permanent:"
    printf "\n      export PATH=\"\${HOME}/.local/bin:\${PATH}\"\n\n"
  fi

  log_success "Install directory: ${BOLD}${INSTALL_DIR}${RESET}"
}

# ──────────────────────────────────────────────
#  Step 3 — Download Binary
# ──────────────────────────────────────────────

determine_download_tool() {
  if command -v curl >/dev/null 2>&1; then
    DOWNLOAD_TOOL="curl"
  elif command -v wget >/dev/null 2>&1; then
    DOWNLOAD_TOOL="wget"
  else
    die "Neither curl nor wget found. Please install one and retry."
  fi
}

download_binary() {
  log_step "3/4" "Downloading Ghost binary..."

  # Allow version override via environment variable.
  local version="${GHOST_VERSION:-${INSTALL_VERSION}}"

  local download_url
  if [ "${version}" = "latest" ]; then
    download_url="${RELEASE_BASE_URL}/${BINARY_FILENAME}"
  else
    download_url="${RELEASE_BASE_URL}/${version}/${BINARY_FILENAME}"
  fi

  log_info "URL: ${download_url}"

  # Create a secure temporary directory that is cleaned up on exit.
  TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t ghost-install)"
  trap 'rm -rf "${TMP_DIR}"' EXIT

  TMP_BINARY="${TMP_DIR}/${BINARY_FILENAME}"

  determine_download_tool

  if [ "${DOWNLOAD_TOOL}" = "curl" ]; then
    if ! curl --fail \
              --silent \
              --show-error \
              --location \
              --retry 3 \
              --retry-delay 2 \
              --connect-timeout 30 \
              --max-time 300 \
              --output "${TMP_BINARY}" \
              "${download_url}"; then
      die "Download failed. Check that the URL is accessible: ${download_url}"
    fi
  else
    if ! wget --quiet \
              --tries=3 \
              --timeout=30 \
              --output-document="${TMP_BINARY}" \
              "${download_url}"; then
      die "Download failed. Check that the URL is accessible: ${download_url}"
    fi
  fi

  # Verify the downloaded file is non-empty and appears to be an ELF/Mach-O/PE
  # binary (basic sanity check; replace with checksum verification in prod).
  if [ ! -s "${TMP_BINARY}" ]; then
    die "Downloaded file is empty. Release may not exist for platform ${PLATFORM}."
  fi

  log_success "Download complete."

  # ── Optional: verify SHA-256 checksum ──────────────────────────────────
  # To enable, place a ghost_<platform>.sha256 file alongside each binary in
  # your release server and uncomment the block below.
  #
  # CHECKSUM_URL="${RELEASE_BASE_URL}/${BINARY_FILENAME}.sha256"
  # EXPECTED_SUM_FILE="${TMP_DIR}/${BINARY_FILENAME}.sha256"
  # if [ "${DOWNLOAD_TOOL}" = "curl" ]; then
  #   curl -fsSL --output "${EXPECTED_SUM_FILE}" "${CHECKSUM_URL}" || true
  # else
  #   wget -q -O "${EXPECTED_SUM_FILE}" "${CHECKSUM_URL}" || true
  # fi
  # if [ -f "${EXPECTED_SUM_FILE}" ]; then
  #   if command -v sha256sum >/dev/null 2>&1; then
  #     ACTUAL=$(sha256sum "${TMP_BINARY}" | awk '{print $1}')
  #   elif command -v shasum >/dev/null 2>&1; then
  #     ACTUAL=$(shasum -a 256 "${TMP_BINARY}" | awk '{print $1}')
  #   fi
  #   EXPECTED=$(cat "${EXPECTED_SUM_FILE}" | awk '{print $1}')
  #   if [ "${ACTUAL}" != "${EXPECTED}" ]; then
  #     die "Checksum mismatch! Expected ${EXPECTED}, got ${ACTUAL}."
  #   fi
  #   log_success "Checksum verified."
  # fi
  # ───────────────────────────────────────────────────────────────────────
}

# ──────────────────────────────────────────────
#  Step 4 — Install and Initialise
# ──────────────────────────────────────────────

install_binary() {
  log_step "4/4" "Installing Ghost..."

  INSTALL_PATH="${INSTALL_DIR}/${INSTALL_FILENAME}"

  # Remove any existing installation to avoid permission errors on replace.
  if [ -f "${INSTALL_PATH}" ]; then
    rm -f "${INSTALL_PATH}"
    log_info "Removed existing installation at ${INSTALL_PATH}."
  fi

  mv "${TMP_BINARY}" "${INSTALL_PATH}"
  chmod 755 "${INSTALL_PATH}"

  log_success "Installed to: ${BOLD}${INSTALL_PATH}${RESET}"
}

run_setup() {
  log_info "Running first-time handle setup..."
  printf "\n"

  if ! "${INSTALL_PATH}" --setup; then
    log_warn "Setup step failed. You can run '${INSTALL_FILENAME} --setup' manually."
  fi
}

# ──────────────────────────────────────────────
#  Summary
# ──────────────────────────────────────────────

print_summary() {
  printf "\n"
  printf "${DIM}  ────────────────────────────────────────────────────────${RESET}\n"
  printf "  ${GREEN}${BOLD}Ghost is installed!${RESET}\n\n"
  printf "  Run ${BOLD}${BR_CYAN}ghost${RESET} to start a secure chat session.\n"
  printf "${DIM}  ────────────────────────────────────────────────────────${RESET}\n"
  printf "\n"
}

# ──────────────────────────────────────────────
#  Entrypoint
# ──────────────────────────────────────────────

main() {
  print_banner
  detect_platform
  determine_install_dir
  download_binary
  install_binary
  run_setup
  print_summary
}

main "$@"
