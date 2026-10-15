# 同面板完成后发送的链式队列 + 优先级 — 设计

日期：2026-10-15
当前版本：v0.4.0（`cd82bc0`）
目标版本：v0.5.0

## 问题

同一个面板上有多条"完成后发送"（`after_completion`）消息时，期望它们**按队列依次发送**：上一条被 agent 做完、发出后，才轮到下一条。当前实现做不到，且是静默错的。

## 已验证事实（现状行为，非假设）

| 事实 | 依据 |
|---|---|
| `Step` 逐条独立 `advance`，同面板多条 `Waiting` 在同一 tick 各自判定 | `engine.go:163` 循环，无跨消息协调 |
| 同面板多条消息**共享同一个 `state_change_seq`**：agent 一次 idle 会让所有越过基线的消息同 tick 触发 | `advanceCompletion` 每条独立比 `pane.StateChangeSeq > m.BaselineSeq`（`engine.go:341`） |
| 触发顺序 = `Store.List()` 顺序（收藏→状态档→时间→id），与用户意图无关 | `store.go:170` `Sort` |
| 结论：现在"两条完成后消息"= 一次完成**一起发**，不是依次；顺序不可控 | 综合以上 |

推论：这是 v0.3.0 设计里明确"不在本次范围"的"per-pane 串行化"，现在要做。

## 决策（与用户确认）

- **语义：真链式** —— 一次 agent 完成只放行**一条**（队头）；下一条等**再下一次** working→idle 边沿。
- **优先级模型：面板内位置序号** —— 每条消息一个 `queue_pos`，同面板按它升序发送；不是抽象"高/中/低"档。
- **参与范围：只链 `after_completion`** —— 手动（`s` 即时）与定时（`scheduled`）不进队、不占位、不阻塞。
- **排序都影响发送** —— 列表显示顺序与引擎发送顺序读同一个 `queue_pos`，不各排各的。
- **失败暂停链**（设计者主张，待 review 可推翻）—— 队头 `dispatch` 失败转 `Failed` 后，**该面板的链冻结**，不自动放行下一条；因为队头的任务没送达 agent，跳过它发下一条是错的。用户 `s` 重试或删除该失败消息后，链自动续上。

## 方案选择

采用 **A：`queue_pos` 字段 + 队头门控**。给消息加持久化的面板内序号，引擎每组只放行最小者，UI 提供 `[`/`]` 调序。

否掉：
- **B：靠 `UpdatedAt` 时间隐式排队** —— 无法手动插队，用户要的是"可调优先级"，时间序不满足。
- **C：全局优先级整数** —— 跨面板比较无意义（不同面板各自排队），整数还引入空洞/重排成本；面板内序号天然稠密。

## 设计

### 1. 数据模型

`model.Message` 新增：

```go
// QueuePos is a message's 1-based slot in its pane's after_completion queue.
// 0 means "not in a queue": manual, scheduled, or never armed as after_completion.
// Assigned once on first entry and kept across re-arms and failures, so a retried
// message returns to its original place. Lower fires first; gaps are fine.
QueuePos int `yaml:"queue_pos,omitempty"`
```

- **分配一次**：`Engine.armFrom`（`engine.go:424`）里，当一条 after_completion 消息进入 Waiting 且 `QueuePos==0`（即首次入队）时，扫描同面板（`QualifiedID` 相同）在队消息取 `max+1`（1-based，首位为 1）。之后重 arm（改目标、或把失败消息用 `t` 重新设回 after_completion 回到 Waiting）**保持原 `QueuePos`**——这正是"重试的队头回到原位继续挡后继"的前提。
- **离队归零**：消息 `Sent`、被删除、或触发方式改成非 after_completion 时 `QueuePos=0`。`s`（`SendNow`）是**立即抢发**：成功即 `Sent` 离队、后继补位；这是"插队即抢发"的显式出口，无需额外的跳过命令。重新排队靠 `t` 重设触发，不是 `s`。
- `omitempty`：`QueuePos==0` 不落盘，手动/定时/单条消息与存量文件不改写。

### 2. 引擎：队头门控 + 链式推进

`Engine.Step`（`engine.go:145`）在现有逐条循环**之前**，先算出"本轮每个面板的队头"：

```
heads := map[paneQualifiedID]messageID   // 每面板 QueuePos 最小的 Waiting after_completion
```

`advanceCompletion` 在 `snap.Resolve` + `bindTarget` 之后（engine.go:286，此时才拿得到 `pane`，门控需要它的 `QualifiedID` 与 `StateChangeSeq`）加一道门：若本消息不是其面板的队头，进入**冻结**——不 dispatch、不发 `EventArmed`，但**每 tick 把自己的基线刷新为当前 `pane.StateChangeSeq`**（`BaselineSeq=seq; BaselineSet=true; ObservedWorking=false; SettleSince=nil`）。队头逻辑不变（边沿检测、settle、dispatch）。

**链式前进（靠冻结期刷新，而非跨消息改写）**：后继在排队期间基线始终跟随当前 seq，所以它记录的"上一次已知状态"就是它即将接棒的那一刻。当队头 `dispatchLocked` 成功转 `Sent`、下一 tick 后继成为新队头时，它的基线已是"接棒时刻"的 seq，于是自然等**下一个** working→idle 边沿才发——不会复用触发上一条的那次完成，也不会因基线陈旧而误判"已越过"提前发。这个机制只改本消息自己的字段，不跨消息写、不依赖 `Step` 的循环顺序。

**失败暂停**：队头 dispatch 失败转 `Failed`（离开 Waiting，但**保留 `QueuePos`**）。按决策链**不自动越过未送达的队头**：`heads` 计算时，只要某面板还有 `Failed` 的 after_completion 消息，该面板本轮**无队头**（全冻结）。解冻由用户完成——`s` 立即重试（成功→`Sent`→链续；再失败→仍 `Failed`→仍冻结），或 `t` 重新设回 after_completion 回到 Waiting 原位，或删除该消息。

### 3. UI：呈现 + 调序

- **徽标**：`listRows`（`view.go:219`）在"队列中"tab 下、after_completion 消息的 title 前显示面板内序号 `①②`（该消息在其面板 Waiting 队列里按 `queue_pos` 的排名）。非队列中消息不显示。序号走 `fit`/`padBlock` 的同一宽度测量路径（与状态图标一致），避免 title 行与 meta 行错位；超过圈码上限时回退 `#N`。
- **快捷键**：`[` 上移、`]` 下移。作用于光标所在消息：与**同面板**相邻 `queue_pos` 的 after_completion 消息交换序号；已是首/末位或非队列中消息则提示、不改。走 `provider.SetQueuePos(id, delta)` 持久化并 `loadMsg` 刷新。
- **排序一致**：`store.Sort`（`store.go:170`）在同一状态档内，对同面板 after_completion 按 `QueuePos` 升序（同值回退 `CreatedAt`）。列表顺序 = 发送顺序。

### 4. Wire / Provider

- `api.Message` 加 `QueuePos`；server `toAPIMessage`/`fromAPIMessage`、client 同名函数透传。
- `dataprovider.DataProvider` 加 `SetQueuePos(ctx, id string, delta int) (*model.Message, error)`；`LocalProvider` 直改 store，`Client` 走 `PUT /api/messages/{id}/queue-pos`。
- 远端 TUI 与本地一致。

## 测试

- **引擎链式**：同面板两条 after_completion（pos 1、2）。agent working→idle 一次 → 只有 pos 1 发出，pos 2 仍 Waiting；再次 working→idle → pos 2 发出。断言 `fake.Sends()` 分两轮、顺序正确，且 pos 2 不复用触发 pos 1 的那次边沿。
- **队头门控**：三条同面板，一次完成只发队头；其余冻结（不发、不 `EventArmed`），但基线每 tick 刷新为当前 seq（断言冻结消息 `BaselineSeq` 跟随 seq 前进）。
- **失败暂停**：队头 dispatch 注入失败 → 转 Failed 且保留 `QueuePos`，该面板全冻结、后续不自动发；`s` 立即重试成功后 → `Sent` → 链续上。
- **调序**：`[`/`]` 交换同面板相邻 `queue_pos`；跨面板不误伤；首/末位无效果。
- **分配**：新入队取 max+1（1-based，首条为 1）；已 Waiting 重 arm 或失败后用 `t` 重设回队列，均保持原 `QueuePos`；`Sent`/删除/改非 after_completion → 归 0。
- **排序一致**：`store.Sort` 下同面板 after_completion 按 `queue_pos` 升序，与 `heads` 选取一致。
- **wire 往返**：`QueuePos` 过 server→client 不丢。
- **不参与**：手动、定时消息永不出现在 `heads`，其 `queue_pos` 恒 0。

## 兼容性

- 存量消息无 `queue_pos` → 读为 0；同面板多条 0 时按 `CreatedAt` 稳定排序，行为退化为"按入队时间 FIFO"，与今天最接近，不破坏。
- `after_completion` 单条消息（绝大多数现状）：它自己就是队头，链式逻辑对其无感，行为不变。
- `queue_pos` 仅对 `Waiting`+`after_completion` 有语义；其它状态忽略该字段，不新增状态机分支。

## 不在本次范围

- 手动/定时消息与 after_completion 混排的跨类型优先级（决策明确"只链 after_completion"）。
- 队列的批量重排 UI（拖拽等）；本次只给 `[`/`]` 单步。
- 失败暂停链的"跳过此条继续"选项（本次失败即冻结，需用户显式重试/删除）。
