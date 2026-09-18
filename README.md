# herdr-outbox

herdr 的 Agent 消息发件箱：把要交给 Agent 的事先记下来，决定**什么时候、发给哪个面板、发什么**。

## 场景

Agent 正在跑当前任务，你已经想到后面还有一串事：重构模块、补单元测试、查缓存、修异常、最后完整测一遍。一口气全发过去容易乱，靠脑子记又容易忘。

把这些事逐条记进发件箱，按节奏发给 Agent：

- **它忙完自动发**：Agent 完成当前任务进入空闲时，下一条自动送过去
- **定时发**：`20m`、`09:30`、`2026-09-17 02:00`，到点自动发
- **手动发**：TUI 里选中按 `s`
- **先记着不发**：存草稿或收藏，随时改随时看

```
你 → herdr-outbox（排队 · 定时 · 完成检测） → herdr → Agent
```

## 功能

- **TUI 队列**：左侧消息列表、右侧详情、底部 herdr 实时状态，`?` 看快捷键
- **三种触发**：手动 / 代理完成后 / 定时；Server 常驻后台，TUI 关了照样调度
- **面板选择器**：从 herdr 实时拓扑挑目标，可预览面板内容，不用手抄 ID
- **文件存储**：每条消息一个 Markdown 文件，没有数据库，目录可直接进 git

## 安装

Linux / macOS：

```bash
curl -fsSL https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.sh | sh
```

Windows (PowerShell)：

```powershell
irm https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.ps1 | iex
```

或从源码：`git clone` 后 `make install`；卸载用 `uninstall.sh` / `make uninstall`。

## 上手

```bash
herdr-outbox          # 打开 TUI；服务没起会自动拉起
```

常用路径：`n` 新建 → `p` 选面板 → `s` 发送；`t` 改触发方式，`e` 编辑内容。编辑方式默认在 TUI 内，想用 `$EDITOR` 就 `herdr-outbox config editor external`。

服务单独管：

```bash
herdr-outbox server          # 启动后台服务（调度循环在这里）
herdr-outbox server stop     # 停止，等进程真正退出
herdr-outbox server status
```

## 命令

| 命令 | 说明 |
| --- | --- |
| `new [文本…]` | 新建草稿 |
| `ls [--json]` | 列出消息 |
| `send <id\|all-ready>` | 立即发送 |
| `arm <id> <触发>` | 设触发：`manual` / `completion` / `20m` / `09:30` / `2026-09-17 02:00` |
| `target <id> <面板>` | 指定目标（`wK:p1` 或面板标签） |
| `log [n]` · `panes` · `doctor` | 调度日志 / herdr 拓扑 / 连接体检 |
| `config editor builtin\|external` | 切换编辑方式 |
| `version` · `update` · `uninstall [--data]` | 版本 / 自更新 / 卸载 |

全局选项：`--dir`（消息目录，或 `$HERDR_OUTBOX_DIR`）、`--session`、`--bin`、`--dry-run`（模拟后端，不真发）、`--interval`。

## 触发方式

- **手动**：按 `s` 或 `send` 时发
- **完成后**：记录目标代理的状态基线，等它从 working 稳定转为空闲后发一次；带防抖，状态抖动不会重复发
- **定时**：相对时间（`20m`、`1h30m`、`in 2 hours`）、当天时刻（`09:30`，已过则顺延明天）或完整日期（`09-17 02:00`、`2026-09-17 02:00`）

herdr 暂时连不上时，触发设置照样保存，后端恢复后自动补基线，不会丢。

## 存储

消息目录下每条消息一个 `.md` 文件：`---` 包起来的 YAML frontmatter 存元数据，之后是正文（即发给代理的内容）。编辑器只改正文；手改 frontmatter 也有效，程序每次都从磁盘重读。其余文件：`dispatch-log.jsonl`（调度记录）、`config.yaml`、`server.log`。

## 开发

```bash
make build     # 构建到当前目录（版本号由 git describe 注入）
make test      # go test ./...
make dist      # 交叉编译到 dist/
```

Windows 上改完代码：`make build` 后先 `.\herdr-outbox.exe server stop` 再重启服务，TUI 也要退出重开——旧进程不会自动换代码。

## 依赖

- Go 1.26+
- [herdr](https://herdr.dev) - 终端工作区与 Agent 管理

## License

MIT
