# herdr-outbox

TUI 消息发件箱，用于向 [herdr](https://herdr.dev) 管理的代理面板发送消息。支持定时发送、代理完成后自动发送等调度功能。

## 功能

- **TUI 界面**：基于 Bubble Tea 的终端 UI，管理消息队列
- **Client-Server 架构**：Server 常驻后台运行调度，TUI 作为客户端连接
- **多种触发方式**：手动发送、代理完成后发送、定时发送
- **面板预览**：选择目标面板时可预览终端内容
- **跨平台**：支持 Windows、Linux、macOS

## 安装

### 从源码安装（推荐）

**Linux/macOS:**
```bash
curl -fsSL https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.sh | sh
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.ps1 | iex
```

### 使用 Make

```bash
git clone https://github.com/ParadiseWitch/herdr-outbox.git
cd herdr-outbox
make install
```

### 卸载

```bash
# Linux/macOS
curl -fsSL https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/uninstall.sh | sh

# 或使用 make
make uninstall
```

## 使用

### 启动 Server

```bash
herdr-outbox server
```

Server 会在后台运行调度循环，即使 TUI 退出也会继续处理定时任务。

### 启动 TUI

```bash
herdr-outbox
```

### 快捷键

| 按键 | 功能 |
|------|------|
| `n` / `e` / `Enter` | 新建消息 / 编辑 |
| `i` | 重命名标题 |
| `s` | 立即发送 |
| `t` | 设置触发方式 |
| `p` | 选择目标面板 |
| `l` | 查看调度日志 |
| `q` | 退出 |

## 开发

```bash
# 构建
make build

# 运行测试
make test

# 交叉编译所有平台
make dist
```

## 架构

```
┌─────────────────────┐         HTTP/JSON          ┌─────────────────────┐
│  TUI (client)       │ ◄────────────────────────► │  Server              │
│  - 纯 UI 展示       │   REST API + SSE events    │  - Store + Ledger    │
│  - 用户操作 → API   │                             │  - Engine 调度循环   │
│  - SSE 接收事件     │                             │  - herdr Backend     │
└─────────────────────┘                             └─────────────────────┘
```

## 依赖

- Go 1.26+
- [herdr](https://herdr.dev) - 终端工作区管理

## License

MIT
