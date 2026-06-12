#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════╗
# ║                   Ghost CLI — Single-Command Uninstaller                 ║
# ║                                                                          ║
# ║  Usage:                                                                  ║
# ║    curl -fsSL https://ghost.tirup.in/uninstall.sh | bash                 ║
# ╚══════════════════════════════════════════════════════════════════════════╝

set -euo pipefail

BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
CYAN="\033[36m"
BR_CYAN="\033[96m"

print_banner() {
  printf "\n"
  printf "${BOLD}${BR_CYAN}  ██████╗ ██╗  ██╗ ██████╗ ███████╗████████╗${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ██╔════╝ ██║  ██║██╔═══██╗██╔════╝╚══██╔══╝${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ██║  ███╗███████║██║   ██║███████╗   ██║   ${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ██║   ██║██╔══██║██║   ██║╚════██║   ██║   ${RESET}\n"
  printf "${BOLD}${BR_CYAN}  ╚██████╔╝██║  ██║╚██████╔╝███████║   ██║   ${RESET}\n"
  printf "${BOLD}${BR_CYAN}   ╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚══════╝   ╚═╝   ${RESET}\n"
  printf "\n"
  printf "${DIM}  Ephemeral · Encrypted · Zero-Knowledge Chat — Uninstaller${RESET}\n"
  printf "${DIM}  ──────────────────────────────────────────────────────────${RESET}\n"
  printf "\n"
}

log_info()    { printf "  ${CYAN}→${RESET}  %s\n" "$1"; }
log_success() { printf "  ${GREEN}✓${RESET}  %s\n" "$1"; }
log_warn()    { printf "  ${YELLOW}!${RESET}  %s\n" "$1"; }
log_error()   { printf "  ${RED}✗${RESET}  %s\n" "$1" >&2; }

main() {
  print_banner

  # 1. Locate and remove the binary
  local removed=0
  local paths=(
    "${HOME}/.local/bin/ghost"
    "${HOME}/bin/ghost"
    "/usr/local/bin/ghost"
  )

  for path in "${paths[@]}"; do
    if [ -f "${path}" ]; then
      log_info "Removing Ghost binary at ${path}..."
      if rm -f "${path}"; then
        log_success "Removed: ${path}"
        removed=1
      else
        log_error "Failed to remove binary at ${path} (check permissions)."
      fi
    fi
  done

  # 2. Remove configuration
  if [ -d "${HOME}/.ghost" ]; then
    log_info "Removing configuration folder ${HOME}/.ghost..."
    if rm -rf "${HOME}/.ghost"; then
      log_success "Removed: ${HOME}/.ghost"
    else
      log_error "Failed to remove config folder ${HOME}/.ghost"
    fi
  else
    log_info "No configuration folder found."
  fi

  printf "\n"
  printf "${DIM}  ──────────────────────────────────────────────────────────${RESET}\n"
  printf "  ${GREEN}${BOLD}Ghost CLI has been successfully uninstalled.${RESET}\n"
  printf "${DIM}  ──────────────────────────────────────────────────────────${RESET}\n"
  printf "\n"
}

main "$@"
