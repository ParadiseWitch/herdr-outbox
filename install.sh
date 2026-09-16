#!/bin/sh
set -e

# herdr-outbox installer
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/.../install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/.../install.sh | sh -s -- --dir /usr/local/bin
#
# Environment variables:
#   HERDR_OUTBOX_INSTALL_DIR  - install directory (default: ~/.local/bin)
#   HERDR_OUTBOX_SOURCE       - clone from this URL
#   HERDR_OUTBOX_VERSION      - git tag/branch to install (default: main)

REPO_URL="${HERDR_OUTBOX_SOURCE:-https://github.com/ParadiseWitch/herdr-outbox.git}"
VERSION="${HERDR_OUTBOX_VERSION:-main}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

detect_os() {
  case "$(uname -s)" in
    Linux*)   echo "linux";;
    Darwin*)  echo "darwin";;
    MINGW*|MSYS*|CYGWIN*) echo "windows";;
    *)        err "不支持的操作系统: $(uname -s)";;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64";;
    aarch64|arm64)   echo "arm64";;
    *)               err "不支持的架构: $(uname -m)";;
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
  elif [ -w "$HOME/.local/bin" ] || [ ! -d "$HOME/.local/bin" ]; then
    echo "$HOME/.local/bin"
  elif [ -w "/usr/local/bin" ]; then
    echo "/usr/local/bin"
  else
    echo "$HOME/.local/bin"
  fi
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    return 1
  fi
  return 0
}

check_go() {
  if need_cmd go; then
    go_version="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
    info "检测到 Go $go_version"
    return 0
  fi
  return 1
}

check_git() {
  if need_cmd git; then
    info "检测到 git $(git --version | awk '{print $3}')"
    return 0
  fi
  return 1
}

install_from_source() {
  install_dir="$1"
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  if check_git; then
    info "克隆仓库 ($VERSION)…"
    git clone --depth 1 --branch "$VERSION" "$REPO_URL" "$tmpdir/herdr-outbox" 2>/dev/null || \
    git clone --depth 1 "$REPO_URL" "$tmpdir/herdr-outbox"
  else
    err "需要 git 来下载源码。请先安装 git: https://git-scm.com/"
  fi

  if ! check_go; then
    err "需要 Go 来编译。请先安装 Go: https://go.dev/dl/"
  fi

  info "编译 herdr-outbox…"
  cd "$tmpdir/herdr-outbox"
  ext=""
  os="$(detect_os)"
  if [ "$os" = "windows" ]; then ext=".exe"; fi

  go build -o "$tmpdir/herdr-outbox${ext}" ./cmd/herdr-outbox

  mkdir -p "$install_dir"
  binary="herdr-outbox${ext}"
  cp "$tmpdir/$binary" "$install_dir/$binary"
  chmod +x "$install_dir/$binary"

  info "已安装 $install_dir/$binary"
}

install_from_go() {
  install_dir="$1"

  if ! check_go; then
    err "需要 Go。请先安装 Go: https://go.dev/dl/"
  fi

  info "使用 go install 安装…"
  GOBIN="$install_dir" go install "herdr-outbox/cmd/herdr-outbox@${VERSION}"

  info "已安装到 $install_dir"
}

add_to_path() {
  dir="$1"
  case ":$PATH:" in
    *":$dir:"*) return;;
  esac

  shell_rc=""
  if [ -n "$ZSH_VERSION" ] || [ "$(basename "$SHELL")" = "zsh" ]; then
    shell_rc="$HOME/.zshrc"
  elif [ -n "$BASH_VERSION" ] || [ "$(basename "$SHELL")" = "bash" ]; then
    if [ -f "$HOME/.bash_profile" ]; then
      shell_rc="$HOME/.bash_profile"
    else
      shell_rc="$HOME/.bashrc"
    fi
  fi

  if [ -n "$shell_rc" ]; then
    export_line="export PATH=\"$dir:\$PATH\""
    if ! grep -qF "$dir" "$shell_rc" 2>/dev/null; then
      echo "" >> "$shell_rc"
      echo "# herdr-outbox" >> "$shell_rc"
      echo "$export_line" >> "$shell_rc"
      info "已添加 $dir 到 PATH ($shell_rc)"
      info "请运行: source $shell_rc"
    fi
  else
    warn "请将 $dir 添加到 PATH"
  fi
}

main() {
  parse_args "$@"

  info "herdr-outbox 安装程序"
  echo ""

  os="$(detect_os)"
  arch="$(detect_arch)"
  install_dir="$(detect_install_dir)"

  info "系统: $os/$arch"
  info "安装目录: $install_dir"
  echo ""

  mkdir -p "$install_dir"

  if [ "$VERSION" = "latest" ] || [ "$VERSION" = "main" ]; then
    install_from_source "$install_dir"
  else
    install_from_go "$install_dir"
  fi

  add_to_path "$install_dir"

  echo ""
  info "安装完成！"
  info "运行 'herdr-outbox help' 查看用法"
  info "运行 'herdr-outbox server' 启动后台服务"
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --dir)
        HERDR_OUTBOX_INSTALL_DIR="$2"
        shift 2
        ;;
      --version)
        HERDR_OUTBOX_VERSION="$2"
        VERSION="$2"
        shift 2
        ;;
      --source)
        HERDR_OUTBOX_SOURCE="$2"
        REPO_URL="$2"
        shift 2
        ;;
      --help|-h)
        echo "用法: install.sh [选项]"
        echo ""
        echo "选项:"
        echo "  --dir PATH       安装目录 (默认: ~/.local/bin)"
        echo "  --version TAG    安装指定版本 (默认: main)"
        echo "  --source URL     从指定仓库克隆"
        echo "  --help           显示帮助"
        exit 0
        ;;
      *)
        shift
        ;;
    esac
  done
}

main "$@"
