#!/bin/sh
set -e

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }

detect_os() {
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) echo "windows";;
    *)                    echo "unix";;
  esac
}

detect_install_dir() {
  if [ -n "$HERDR_OUTBOX_INSTALL_DIR" ]; then
    echo "$HERDR_OUTBOX_INSTALL_DIR"
    return
  fi
  os="$(detect_os)"
  if [ "$os" = "windows" ]; then
    echo "${LOCALAPPDATA:-$HOME/.local}/bin"
  else
    echo "$HOME/.local/bin"
  fi
}

remove_from_path() {
  dir="$1"
  shell_rc=""
  if [ -n "$ZSH_VERSION" ] || [ "$(basename "$SHELL" 2>/dev/null)" = "zsh" ]; then
    shell_rc="$HOME/.zshrc"
  elif [ -n "$BASH_VERSION" ] || [ "$(basename "$SHELL" 2>/dev/null)" = "bash" ]; then
    if [ -f "$HOME/.bash_profile" ]; then
      shell_rc="$HOME/.bash_profile"
    else
      shell_rc="$HOME/.bashrc"
    fi
  fi

  if [ -n "$shell_rc" ] && [ -f "$shell_rc" ]; then
    if grep -qF "# herdr-outbox" "$shell_rc" 2>/dev/null; then
      sed -i.bak '/# herdr-outbox/d;/herdr-outbox/d' "$shell_rc"
      rm -f "${shell_rc}.bak"
      info "已从 $shell_rc 移除 PATH 配置"
    fi
  fi
}

main() {
  info "卸载 herdr-outbox"
  
  install_dir="$(detect_install_dir)"
  
  os="$(detect_os)"
  if [ "$os" = "windows" ]; then
    binary="herdr-outbox.exe"
  else
    binary="herdr-outbox"
  fi
  
  target="$install_dir/$binary"
  
  if [ -f "$target" ]; then
    rm -f "$target"
    info "已删除 $target"
  else
    warn "未找到 $target"
  fi
  
  # Also try common locations
  rm -f /usr/local/bin/herdr-outbox 2>/dev/null || true
  rm -f /usr/local/bin/herdr-outbox.exe 2>/dev/null || true
  
  remove_from_path "$install_dir"
  
  info "卸载完成"
  info "数据目录未删除。如需清理，请手动删除 ~/.local/share/herdr-outbox/"
}

main "$@"
