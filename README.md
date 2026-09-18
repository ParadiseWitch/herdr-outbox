# herdr-outbox

herdr 的 Agent 消息发件箱。提前把要交给 Agent 的事情记下来，决定**什么时候、发给哪个面板、发什么内容**——手动发、等它忙完再发、定时发。

herdr 负责运行 Agent，herdr-outbox 负责排队和调度。

## 功能

- **TUI 队列管理**：基于 Bubble Tea，左侧发件箱、右侧消息详情、底部 herdr 实时状态
- **Client / Server 架构**：Server 常驻后台跑调度循环，TUI 退出也不影响定时任务
- **三种触发方式**：手动、目标代理完成后、指定时间
- **面板选择器**：从 herdr 实时拓扑里挑目标，可预览面板内容，不用手抄 ID
- **两种编辑方式**：TUI 内置编辑器，或交给 `$EDITOR`
- **文件存储**：每条消息是一个 Markdown 文件（YAML frontmatter + 正文），没有数据库，可以直接 git 管理和手工编辑
- **跨平台**：Windows、Linux、macOS；`--dry-run` 内置模拟后端可离线试用

## 安装

### 脚本安装

**Linux / macOS：**

```bash
curl -fsSL https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.sh | sh
```

**Windows (PowerShell)：**

```powershell
irm https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.ps1 | iex
```

安装指定版本（版本号为 git tag）：

```bash
curl -fsSL .../install.sh | sh -s -- --version v0.1.0
HERDR_OUTBOX_VERSION=v0.1.0 sh install.sh
```

### 使用 Make

```bash
git clone https://github.com/ParadiseWitch/herdr-outbox.git
cd herdr-outbox
make install        # 构建并装到 ~/.local/bin
```

### 卸载

```bash
curl -fsSL https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/uninstall.sh | sh
make uninstall
herdr-outbox uninstall --data     # 连消息目录一起删
```

## 快速上手

```bash
herdr-outbox server          # 启动后台服务（调度循环在这里）
herdr-outbox                 # 打开 TUI；服务没起时会自动拉起一个
```

在 TUI 里按 `n` 写一条消息，`p` 选目标面板，`s` 立即发送，或 `t` 改成"代理完成后"。

不带 `server` 子命令时，TUI 会自动确保服务在跑，所以日常只需要一条命令。

## 命令行

| 命令 | 说明 |
| --- | --- |
| `herdr-outbox` | 打开 TUI（必要时自动启动服务） |
| `herdr-outbox server` | 启动后台服务 |
| `herdr-outbox server stop` | 停止服务，等待进程真正退出 |
| `herdr-outbox server status` | 查看服务地址、运行时长、消息数 |
| `herdr-outbox ls [--json]` | 列出消息 |
| `herdr-outbox new [文本…]` | 新建草稿 |
| `herdr-outbox send <id\|all-ready>` | 立即发送 |
| `herdr-outbox arm <id> <触发>` | 设置触发：`manual` / `completion` / `20m` / `09:30` / `2026-09-17 02:00` |
| `herdr-outbox target <id> <面板>` | 指定目标面板（`wK:p1`、`qodercli-1`） |
| `herdr-outbox log [n]` | 查看调度日志 |
| `herdr-outbox panes` | 列出 herdr 工作区和面板 |
| `herdr-outbox doctor` | 检查 herdr 连接、路径、代理状态序号 |
| `herdr-outbox config` | 查看配置；`config editor builtin\|external` 修改 |
| `herdr-outbox version` | 显示版本（`--version`、`-V`） |
| `herdr-outbox update` | 从源码目录 `git pull` 重新构建 |
| `herdr-outbox uninstall [--data]` | 卸载程序 |

全局选项：

| 选项 | 说明 |
| --- | --- |
| `--dir PATH` | 消息目录，默认取 `$HERDR_OUTBOX_DIR` 或用户配置目录下的 `herdr-outbox` |
| `--session NAME` | 指定 herdr 会话 |
| `--bin PATH` | 指定 herdr 可执行文件（`$HERDR_BIN`） |
| `--dry-run` | 使用内存模拟后端，不向真实代理发送任何内容 |
| `--interval DURATION` | 调度间隔，默认 `1s` |

## TUI 快捷键

| 按键 | 功能 |
| --- | --- |
| `n` / `e` / `Enter` | 新建消息 / 编辑内容 |
| `i` | 重命名标题（留空则显示内容预览） |
| `j` `k` `g` `G` `tab` | 移动、跳转、在列表和消息区之间切换 |
| `s` | 立即发送 |
| `t` | 选择触发方式（手动 / 完成后 / 定时） |
| `p` | 从 herdr 实时状态选择目标面板 |
| `u` | 暂存为草稿（不发送） |
| `f` `c` `d` | 收藏 / 克隆 / 删除 |
| `x` | 切换筛选（全部、待处理、已发送） |
| `r` `R` | 刷新 herdr 状态 / 立即执行一次调度 |
| `l` | 调度日志 |
| `ctrl+d` `ctrl+u` | 滚动长消息 |
| `q` / `Q` | 退出筛选层 / 退出程序 |
| `?` | 帮助 |

内置编辑器（默认）：`ctrl+s` 保存，`esc` 一次是确认放弃、再按一次才退出。改成外部编辑器：

```bash
herdr-outbox config editor external     # 交给 $EDITOR / $VISUAL
herdr-outbox config editor builtin      # 回到 TUI 内编辑
```

远程模式下外部编辑会先把内容下载到临时文件，编辑完再传回，源文件不留残留。

## 触发方式

| 触发 | 行为 |
| --- | --- |
| `manual` | 按 `s` 或 `send` 时立刻发送 |
| `after_completion` | 等目标代理从 working 变为 idle/done 时发送一次 |
| `scheduled` | 到点发送 |

`scheduled` 接受的时间写法（相对时间最省事）：

```text
20m            1h30m          in 2 hours
09:30          18:05:00       （已过今天则顺延到明天）
09-17 02:00    2026-09-17 02:00    2026-09-17T02:00:00
```

`after_completion` 依赖 herdr 报告的代理状态序号：先记录基线，只有序号发生变化且稳定一段时间才发送，所以不会因状态抖动重复发、也不会把刚挂上去时已经在跑的活当成"刚完成"。`herdr-outbox doctor` 会显式提示读不到序号的情况——那意味着"等待完成"永远不会触发。

herdr 暂时不可达时，触发设置依然会保存下来，等后端恢复后自动补记基线；不会因为一次连不上就丢掉你的选择。

## 配置

配置在消息目录下的 `config.yaml`：

```yaml
editor: builtin      # builtin | external
```

消息目录默认是用户配置目录下的 `herdr-outbox`（Windows：`%APPDATA%\herdr-outbox`；Linux/macOS：`~/.config/herdr-outbox`），可用 `--dir` 或 `$HERDR_OUTBOX_DIR` 覆盖。

## 存储格式

没有数据库。每条消息就是目录下的一个 Markdown 文件：开头是 `---` 包起来的 YAML frontmatter（元数据），之后是正文，正文就是要发给代理的内容。下面是 `herdr-outbox new "…"` 生成的真实文件：

```markdown
---
id: 20260918-160840-cd7c
title: 检查 Redis 缓存，确认刚才的修改没有打挂缓存键
target: {}
trigger:
    kind: manual
    settle_seconds: 3
status: draft
created_at: 2026-09-18T16:08:40.5965284+08:00
updated_at: 2026-09-18T16:08:40.5965284+08:00
---

检查 Redis 缓存，确认刚才的修改没有打挂缓存键
```

- TUI 和编辑器只改 `---` 之后的正文，frontmatter 由程序维护
- 手工编辑 frontmatter 同样有效：程序每次都从磁盘重读，比如把 `status: sent` 改回 `draft`，消息就回到待发
- 所以整个目录可以直接进 git，每条消息对应一个文件、改动对应一次 diff

目录里其余文件不是 Markdown：

| 文件 | 格式 | 用途 |
| --- | --- | --- |
| `dispatch-log.jsonl` | JSON Lines | 每次发送、跳过、推迟、失败的调度记录（幂等依据） |
| `server.log` | 纯文本 | 后台服务日志 |
| `.server-pid` / `.server-port` | 纯文本 | 服务进程发现 |
| `config.yaml` | YAML | 配置 |

调度日志用 `herdr-outbox log` 或 TUI 里的 `l` 查看。

## 架构

```
┌─────────────────────┐         HTTP/JSON          ┌─────────────────────┐
│  TUI (client)       │ ◄────────────────────────► │  Server             │
│  - 纯 UI 展示       │   REST API + SSE events    │  - Store + Ledger   │
│  - 用户操作 → API   │                            │  - Engine 调度循环  │
│  - SSE 接收事件     │                            │  - herdr Backend    │
└─────────────────────┘                            └─────────────────────┘
```

`dataprovider.DataProvider` 是唯一的接缝：本地模式直接包 store + engine，远程模式走 HTTP，TUI 代码两边同一套。herdr 不可达在服务端以 502 表达，客户端把它还原成 `herdr.ErrUnavailable`，这样"没有状态可读"和"请求失败"才是两件事。

## 开发

```bash
make build     # 构建到当前目录
make test      # go test ./...
make lint      # go vet ./...
make dist      # 交叉编译 linux/darwin/windows × amd64/arm64 到 dist/
make clean
```

### Windows 上构建后运行

`make build` 在仓库根目录产出 `herdr-outbox.exe`，就地运行即可：

```powershell
make build
.\herdr-outbox.exe version         # 确认是新构建
.\herdr-outbox.exe server stop     # 有服务在跑就先停，否则新构建不生效
.\herdr-outbox.exe server          # 后台服务（调度循环在这里）
.\herdr-outbox.exe                 # TUI
```

服务是常驻进程，重新构建只会替换磁盘上的文件，旧进程仍在跑旧代码；被占用的旧文件会被改名成 `herdr-outbox.exe~` 留在原地。改完代码务必 `server stop` 后再 `server`，`make clean` 清掉这些产物。

TUI 同样是常驻进程：改了界面代码，光重启服务不够，得退出 TUI 再开。

## 版本与发布

版本号由 `git describe` 在构建时通过 `-X main.version` 注入，`herdr-outbox version` 打印的就是它。发布流程：

```bash
git tag -a v0.1.0 -m "herdr-outbox v0.1.0"
git push origin v0.1.0
make dist
```

## 依赖

- Go 1.26+
- [herdr](https://herdr.dev) - 终端工作区与 Agent 管理

## License

MIT
