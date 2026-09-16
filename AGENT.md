# 开发项目：herdr-outbox

请开发一个围绕 **herdr** 的 Agent 消息队列 / Outbox TUI 工具，项目暂定名为：

**herdr-outbox**

## 1. 项目目标

在使用 Claude Code、Qoder、Codex 等 Agent CLI 时，经常会出现这样的场景：

Agent 正在执行当前任务，我已经想到后续还有很多事情需要让 Agent 做。

例如：

1. 重构当前模块
2. 补充单元测试
3. 检查 Redis 缓存
4. 修改异常处理
5. 最后执行一次完整测试

这些任务并不一定适合一次性全部发送给 Agent。

Agent 自身通常提供简单的 input queue，但它更适合处理“下一条输入”，而不是管理一组尚未整理、尚未决定发送时机的任务。

因此需要一个独立的 **Outbox**：

```text
User
  ↓
herdr-outbox
  ↓
scheduler / queue
  ↓
herdr
  ↓
Agent
```

用户可以提前把想发送给 Agent 的内容记录下来，然后：

- 手动发送
- 立即发送
- 指定时间发送
- Agent 当前任务完成后发送
- 暂时保存，不发送
- 作为收藏 / 临时笔记长期保存

核心思想：

> herdr 负责运行 Agent，herdr-outbox 负责管理“什么时候、给哪个 Agent、发送什么内容”。

---

# 2. 首要任务：先研究 herdr

开始编码之前，请先检查当前环境中已有的 herdr CLI / API 能力。

重点确认：

- 如何获取 workspace
- 如何获取 workspace 中的 pane
- 如何获取 pane 对应的 agent
- 如何获取 agent session
- 如何判断 Agent 当前是否 working / idle / completed
- 如何向指定 pane / agent 发送输入
- 是否存在事件监听机制
- 是否可以持续监控 workspace / pane 状态

请尽量复用 herdr 已有能力，不要重复实现一套 pane/session 管理。

如果某些能力目前 herdr 没有，请先设计一个清晰的 adapter/interface，使 herdr-outbox 与 herdr 解耦。

---

# 3. MVP

第一版不要过度设计。

优先实现一个好用的 TUI：

```text
┌───────────────────────┬──────────────────────────────────────┐
│ OUTBOX                │ MESSAGE                              │
│                       │                                      │
│ ● Refactor API        │ # Refactor API                       │
│ ○ Add unit tests      │                                      │
│ ○ Check Redis         │ 请继续检查刚才修改的 API，            │
│ ○ Fix exception       │ 并补充对应的单元测试。                │
│                       │                                      │
│                       │ target: dev / qoder-2                │
│                       │ trigger: manual                      │
│                       │ status: pending                      │
├───────────────────────┴──────────────────────────────────────┤
│ herdr: dev │ pane: qoder-2 │ agent: qodercli │ working       │
└──────────────────────────────────────────────────────────────┘
```

左侧：

**Outbox 消息列表**

右侧：

**当前消息详情 / 编辑内容**

底部：

**当前 herdr workspace / pane / agent 状态**

---

# 4. Message 数据模型

一个 Message 至少应该包含：

```text
id
content
target
trigger
status
created_at
updated_at
```

其中 target 至少支持：

```text
workspace
pane
```

例如：

```yaml
target:
  workspace: dev
  pane: qoder-2
```

trigger 第一版至少考虑：

```text
manual
scheduled
after_completion
```

status 至少：

```text
draft
pending
scheduled
waiting
sending
sent
failed
```

数据格式优先考虑简单、可读、容易备份的方式，例如：

```text
Markdown + YAML frontmatter
```

不要为了 MVP 引入复杂数据库，除非确实有必要。

---

# 5. 消息编辑

这是非常重要的功能。

**不要自己实现复杂的多行文本编辑器。**

当用户选择消息并执行编辑时，优先使用系统 `$EDITOR`：

```text
$EDITOR message.md
```

如果用户环境中是 nvim，就直接：

```text
nvim message.md
```

编辑完成后重新回到 TUI。

需要支持长文本、Markdown、多行内容。

---

# 6. TUI 操作

设计成类似 Vim / lazygit / yazi 的键盘驱动界面。

至少支持：

```text
n       new message
e       edit message
Enter   view / edit
s       send now
d       delete
r       refresh
q       quit
```

后续可以支持：

```text
t       configure trigger
p       configure target
f       favorite
c       clone
```

具体快捷键可以根据 TUI framework 的最佳实践调整。

---

# 7. herdr 集成

TUI 应该能够读取当前 herdr 状态。

例如：

```text
workspace: dev
pane: qoder-2
agent: qodercli
status: working
```

用户创建消息时，可以方便地选择当前 workspace / pane。

最好能够提供类似：

```text
Target
  workspace
    dev
    test
    production

  pane
    qoder-1
    qoder-2
    claude-1
```

不要要求用户手动输入复杂 ID。

---

# 8. After Completion

这是本项目非常重要的功能，但可以作为 MVP 后的第二阶段。

用户可以设置：

```text
trigger = after_completion
```

例如：

```text
Message:
请继续补充刚才修改模块的单元测试。

Target:
workspace = dev
pane = qoder-2

Trigger:
after_completion
```

执行逻辑：

```text
Agent working
      ↓
Agent working
      ↓
Agent idle / completed
      ↓
herdr-outbox detects completion
      ↓
send message
```

需要避免：

- Agent 状态抖动导致重复发送
- 重复发送同一消息
- daemon 重启后重复发送

因此发送操作应该具有幂等性。

---

# 9. Scheduled Message

第二阶段支持：

```text
send_at = 2026-09-17 02:00
```

例如：

```text
02:00
  ↓
herdr-outbox
  ↓
check target
  ↓
send
``
```
