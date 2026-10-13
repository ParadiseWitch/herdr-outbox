# 多会话支持 + 按面板组织消息列表 — 设计

日期：2026-10-13
当前版本：v0.3.0（`edfc2e6`）
目标版本：v0.4.0

## 问题

1. herdr 支持多个 session（本机实测：`default`、`app`、`jx` 运行中；`note`、`org`、`work_scripts` 已停止）。现有 session→workspace→tab→pane 选择器没有 session 层，因此**无法选择其他 session 的面板**，调度器也只看见一个 session。
2. 消息列表是全局的。期望流程是**先选 pane，列表只显示该 pane 的消息**，然后针对该 pane 分析其消息。
3. 需要一个整体过滤器：按 session / tab / pane 收窄列表；默认显示当前 pane 的消息，可清除后显示所有 session 的全部消息。

## 已验证事实（设计的依据，非假设）

| 事实 | 验证方式 |
|---|---|
| 工作区/标签/面板 id 是**每个 session 独立的计数器**，跨 session 会撞车 | 已停止的 `note` 的 `session.json` 里工作区是 `w1`；运行中的 `app` 也是 `w1`。`w1:p1` 在两处指向不同终端 |
| `herdr --session X api snapshot` **一次调用**返回 workspaces+tabs+panes+agents+`focused_pane_id` | 实测 default：15 panes/6 tabs/4 ws/8 agents；`app`：1/1/1/0；耗时 0.14s |
| 该命令在 0.9.1 与 0.9.3 都存在 | 直接跑 0.9.1 二进制确认 |
| `herdr session list --json` 一次给出全部 session 的 `name`/`running`/`socket_path`/`default` | 0.9.1、0.9.3 均确认可用 |
| `--session` 优先级**高于**继承的 `HERDR_SOCKET_PATH` | 设 socket 为 `note`、加 `--session app` → 返回 `flutter_app` |
| popup 命令会收到 `HERDR_ACTIVE_WORKSPACE_ID` / `HERDR_ACTIVE_TAB_ID` / `HERDR_ACTIVE_PANE_ID` / `HERDR_ACTIVE_PANE_CWD` / `HERDR_SOCKET_PATH`，但仍被剥掉 `HERDR_PANE_ID` | herdr `src/app/custom_commands.rs::custom_command_env()`；0.9.3 二进制内含这些字符串 |
| 无代理的 session，`api snapshot` 返回 `agents: []`（正常，不是错误） | `app`、`jx` 实测 |
| 停止的 session 调用返回 `server_not_running` | `--session note api snapshot` 实测 |

推论：现有"全局一个 `AgentSeqError`"的模型在多会话下**必须按 session 判定**，否则 `app`/`jx` 这类没有代理的 session 会让所有"完成后发送"消息被误判为"读不到代理状态序号"。

## 决策（与用户确认）

- 默认范围：**实时跟随焦点**（不锁定）。
- 状态标签：**保留现有 4 个**（全部/当前/未发送/已发送），作用域过滤是正交的第二层。
- 未运行的 session / 已消失的 pane：**树里标灰，仍可筛出历史消息**。

`当前` 标签与"跟随焦点"的默认作用域是同一件事的两个入口，必须有单一机制，否则两处会互相矛盾：

- 选 `当前` → 清空固定作用域并开启跟随；
- 在树里固定选择 session/tab/pane → 关闭跟随，状态标签自动从 `当前` 回到 `全部`；
- 清除过滤器 → 关闭跟随、清空固定选择，状态标签回到持久值，列表显示所有 session 全部消息。

## 方案选择

采用 **A 聚合式 Backend**：新增 `MultiBackend` 实现现有 `herdr.Backend` 接口，engine / store / scheduler 的形状不变。

否掉的方案：

- **B 每 session 一个 Engine**：N 个 ticker、N 条事件流，store 与 server API 都要按 session 切；而"按 pane 看列表"仍需跨 engine 聚合。改动最大、收益不匹配。
- **C 只做单 session 切换**（靠继承的 `HERDR_SOCKET_PATH` 自动定位）：无法实现"按指定 session 过滤"与"跨 session 历史可见"。

## 设计

### 1. 数据模型

`internal/model/message.go`：

```go
type Target struct {
    Session        string `yaml:"session,omitempty"` // 空 = 主会话
    Workspace      string `yaml:"workspace,omitempty"`
    WorkspaceLabel string `yaml:"workspace_label,omitempty"`
    Pane           string `yaml:"pane,omitempty"`    // 裸 id，如 w9:p1
    PaneLabel      string `yaml:"pane_label,omitempty"`
    Agent          string `yaml:"agent,omitempty"`
}
```

- **空 `Session` = 主会话**（`--session` / `HERDR_SOCKET_PATH` / `HERDR_PANE_ID` 选中的那个）。存量 43 条消息无需迁移，语义等于今天的行为。
- 解析成功后 `bindTarget` 把 `pane.Session` 写回存储，新写入自然带 session；旧记录只在下次 arm/刷新时补齐，不批量改写文件。
- 限定 id 形式 `<session>/<pane-id>`（切分规则见 §2）：CLI、`target`、日志与 UI 都用限定 id。

### 2. herdr 包
```go
type Session struct {
    Name        string `json:"name"`
    Running     bool   `json:"running"`
    Socket      string `json:"socket_path"`
    Default     bool   `json:"default"`
    FocusedPane string `json:"focused_pane,omitempty"` // 该 session 自己的焦点，裸 pane id
    Err         string `json:"err,omitempty"`          // 本轮读失败原因；空为健康
}

type Snapshot struct {
    Sessions   []Session
    Workspaces []Workspace
    Tabs       []Tab
    Panes      []Pane
    Primary    string    // 主会话名：跟随焦点与裸 id 的归位
    FetchedAt  time.Time
    Source     string
    // 按 session 记录"读不到 state_change_seq"，替掉原来的全局字段。
    SeqUnavailable map[string]string
}
```

**焦点必须按 session 取，不能沿用现有 `Snapshot.Focused()` 的"返回第一个 focused 面板"**：实测三个 running session 各有自己的焦点（`default`→`w9:p3B`、`app`→`w1:p1`、`jx`→`w4:p1`）。合并快照里"第一个"取决于遍历顺序，会让作用域随机漂到别的 session。新增 `FocusedIn(session string) (Pane, bool)` 按 `pane.Session` 过滤；`Session.FocusedPane` 取自各 session `api snapshot` 的 `focused_pane_id`。

`Workspace`、`Tab`、`Pane` 各加 `Session string`。`Pane.QualifiedID()` 返回 `session/pane-id`。

限定 id 形式 `<session>/<pane-id>`，例 `app/w1:p1`。切分只在**第一个** `/` 处：pane id 只含 `:` 不含 `/`，session 名就是它的目录名（实测 `default`/`app`/`jx`，目录名不允许 `/`），所以最多一个 `/`。无 `/` 的裸 id 归到**主会话**——即 `--session` / 继承 socket / `HERDR_PANE_ID` 选中的那个，**不是字面的 `default` 会话**（`herdr-outbox --session app` 时主会话是 `app`）。

`MultiBackend`（新文件 `internal/herdr/multi.go`）：
- 名册：`session list --json`；主会话由 `--session` / 继承 socket / `HERDR_PANE_ID` 决定（沿用现有 `CLI.Session` 逻辑）。
- 快照：对每个 **running** session 并发执行 `--session X api snapshot`（每个子 CLI 显式带 `--session`，因此继承的 `HERDR_SOCKET_PATH` 不会污染路由）。合并为一个 Snapshot，session 名册始终保留（含停止的，用于标灰）。
- 单个 session 失败 → 该 session 记 `Err`，其余照常返回（**部分成功不算整体失败**）；全部失败 → 返回 `ErrUnavailable`，保持今天"herdr 不可用"的语义。
- 读上限 16 个 running session，超出部分记 `Err: "未读取（超过上限）"`，避免异常扇出。
- `Send` / `Focus` / `ReadPane` 接受限定 id；裸 id 落到主会话（兼容手输与旧数据）。
- 老路径退化：若 `session list --json` 或 `api snapshot` 不可用（极老 herdr），退回单会话 `workspace/pane/tab/agent list`，并在 notice 里说明"多会话不可用，仅主会话"。
- 快照代价：每个 running session 一次子进程（实测 0.14s）。`MultiBackend` 内部按 session 缓存各自快照，TTL 取 engine 传入的 `SnapshotMaxAge`（现 2s），所以 1s tick 不会每秒 spawn N 个进程；并发上限 4。**只有 armed 消息涉及的 session 加主会话必须当轮刷新**，其余 running session 的拓扑供树显示，按 TTL 惰性刷新。

### 3. 调度器

`Step` 仍只调一次 `Snapshot`。新增判定：

- 消息目标所属 session 本轮带 `Err` → **跳过本轮**，不 park、不标失败、不改状态。读失败是暂时的，绝不能变成"发送失败"或"目标已消失"。
- session 健康但面板找不到 → 保持现有 `park(目标面板已消失)`（这是真实信号）。
- `after_completion` 的 `AgentSeqError` 检查改为查 `SeqUnavailable[pane.Session]`。

`Reconcile` 不读快照，无需改动。

### 4. UI

树：**session → 工作区 → tab → pane**（`treeItem.depth` 0–3）。已有的"空格选中整子树"直接复用到 session 与 tab 层。停止的 session、已消失的 pane 用 `stDim` 标灰且不可作为发送目标。

**所有跨 session 可能重复的 key 一律用限定 id**：`treeItem.key`、`picker.expanded` 的键、`messageFilter.Panes` 的键、`picker.selectedPanes` 的键。今天它们用裸 `w.ID` / `tab_id` / `pane.ID`，跨 session 时 `note/w1` 与 `app/w1` 会撞同一个键，展开状态与筛选互相污染。

作用域（新）：

```go
type messageFilter struct {
    Status filterMode
    Panes  map[string]bool // 限定 pane id -> 选中
    Query  string
}
// Model 新增
scopeFollow bool   // true = 跟随焦点（HERDR_ENV=1 时默认开启）
```

实现用 `scopeFollow` 加"`filter.Panes` 非空即固定"表达三态，不需要第三个字段：`scopePanes()` 依次判 `len(Panes) > 0` → 固定、`scopeFollow` → 跟随当前面板、否则不限。跟随但解析不到焦点（首轮快照未到 / herdr 不可达）= 不限，避免空列表。

**当前面板只有一个定义**：`m.currentPaneRef()` = 主会话本轮快照的焦点面板（`snap.FocusedIn(snap.Primary)`）。默认作用域与 `当前` 状态标签都读它，不能有两份真相。

这里有一处**有意的行为变更**（要写进发布说明）：今天 `当前` 比的是 `msg.Target.Pane == HERDR_PANE_ID`，即"跑着这个 TUI 的那个 pane"，是个固定 id；改成跟随焦点后，在 pane 里跑 TUI 时点去别的面板，列表会跟着换。这正是所选"实时跟随焦点"的语义，同时修好 popup：popup 没有 `HERDR_PANE_ID`，今天 `当前` 直接空列表并提示"未在 herdr 面板内运行"，跟随焦点后看到的就是弹出前那个面板的消息。

依据（实测）：popup 不是布局里的 pane，打开它不改动底层焦点（`popup.rs` 只设 `state.popup_pane` / `state.mode`），键盘输入也全归 popup，底层焦点无法移动 —— 所以 popup 期间的 `focused_pane_id` 恰好是弹出前激活的面板。

- `HERDR_PANE_ID` / `HERDR_ACTIVE_PANE_ID` 退为**首轮快照到达前的启动提示**，避免第一帧作用域为空；快照到手后一律以实时焦点为准。
- 无焦点信息（herdr 不可达 / 不在 herdr 里跑）→ `currentPaneRef()` 为空：`当前` 标签照今天提示无法确定，默认作用域为空 = 全部。
- 持久化的 `lastTab: "current"` 继续可用，`loadLastTab` 不改。
- 作用域迁移：未 pin → 作用域 = `currentPaneRef()` 的那一个面板，随快照刷新；在树里固定选 session/tab/pane → 置 pin、作用域 = `filter.Panes`，状态标签从 `当前` 回到 `全部`；清除过滤器 → 清 pin 与 `Panes`，状态标签回到持久值，列表显示所有 session 全部消息。

**清除作用域的入口**：`esc` 已被占用（详情→列表、清提示），不复用。新增顶层键 `\`（实测空闲）清除全部过滤器（pin、`filter.Panes`、`Query`），回到"所有 session 全部消息"；作用域面板（`x`）在零选中时按回车等同清除。状态栏同步显示当前作用域，避免跟随焦点导致列表莫名变化。


### 5. Server / Client / CLI

- `api.SnapshotResponse` 加 `Sessions []Session`；`api.Workspace/Tab/Pane` 加 `Session`；`api.Target` 加 `Session`；全局 `AgentSeqError` → `SeqUnavailable map[string]string`。
- `GET /api/panes/{id}/read` 加 `?session=`（限定 id 含 `/`，不塞进 path）。
- CLI：`target <id> app/w1:p1`；`panes` 按 session 分组；`doctor` 显示 session 数量与各自健康状态。

### 6. 测试

- `MultiBackend` fixture：双 session，**故意让两个 session 都有 `w1:p1`** → 各自解析、投递到各自 socket（回归上面第一条实测事实）。
- 限定 id 路由 / 裸 id 落主会话。
- 某 session 读失败：该 session 的 armed 消息状态不变，另一个 session 正常发送。
- `SeqUnavailable` 按 session：一个 session 无代理不影响另一个的"完成后发送"。
- wire 往返（server→client）不丢 `Sessions` / `Target.Session`。
- UI：跟随焦点 → 树里固定选择 → 清除过滤器 三段状态迁移；`当前` 标签与跟随同一机制；popup 环境（无 `HERDR_PANE_ID`，有 `HERDR_ACTIVE_PANE_ID`）下列表不为空。
- 停止的 session 标灰、不可选为发送目标，但作为筛选条件能筛出历史消息。
- 限定 id 作 key：两个 session 各有 `w1` 工作区时，展开状态与筛选互不污染（展开 `note/w1` 不影响 `app/w1`）。
- `\` 清除作用域后回到全部；`当前` 标签与默认作用域始终解析到同一个面板（同一个 `currentPaneRef`）。
- `targetDisplay` 带 session 前缀。

### 7. 文档

README 的"面板选择器"条目与"上手"段改为 4 层树 + 默认当前面板 + 清除过滤器；`helpView` 的 `p` / `x` / `1-4` 行同步。


## 实现记录（与设计的差异 + 实测发现）

- **`session list --json` 没有信封**（实测，阻塞性）：它直接打印 `{"sessions":[…]}`，而其他命令是 `{"id":…,"result":…}`。`CLI.call` 原先无条件剥信封，于是名册读取恒返回 `empty_result`，`MultiBackend.Snapshot` 静默退回单会话——多会话功能整条链路失效且不报错。现按形状判断（有 `result` 才剥），`TestUnwrapAcceptsBarePayload` 固定该行为。
- **`dispatchLocked` 缺会话判定**（实测）：`sessionHold` 原先只挂在 tick 路径（scheduled / after_completion）上，手动 `s` 走 `SendNow` 绕过它。停止会话的面板不在快照里，`Resolve` 失败 → 消息被标成"未找到目标面板"并置 failed，把"会话没开"说成"消息坏了"。判定移到 `dispatchLocked` 解析面板之前，三条发送路径共用一个闸口。
- **`当前` 标签与跟随不再重复显示**：两者是同一个限制，状态栏只说一次（`filterLabel` 拿到 `scopeLabel` 的 `follows` 返回值判断，不靠字符串前缀猜）。
- **默认目标仍优先 `HERDR_PANE_ID`**，作用域优先实时焦点：`defaultTarget()` 先看本机 pane 再看焦点，`scopePanes()` 只看焦点。前者是"这条消息发给谁"，写错代价高且启动 id 是确定事实；后者是"我在看谁的列表"，跟随焦点才修好 popup。
- **session 前缀只加在非主会话**：`targetLabel` 在名册 >1 会话且目标不是主会话时前缀 `session · `。单会话与主会话输出不变，避免每行都长一截。
- **`HERDR_ACTIVE_PANE_ID` 未使用**：设计里作为 popup 的启动提示，实测焦点面板已覆盖该场景（popup 不改底层焦点），多留一个来源反而可能和实时焦点不一致，故不引入。
- **清除作用域不改 `loadLastTab` 的持久值语义**：`c` 只在 `当前` 被选中时把它挪到 `全部`（否则按键看起来没生效），其余三个标签是正交维度，保持不变。
- **清除键从 `\` 改为 `c`，克隆让位到 `y`**（用户指定）：`c` 原绑克隆，但"清除筛选"是更高频动作且 `c`(lear) 比反斜杠好记；克隆挪到 `y`，`y` 在浏览态本就空闲（只在删除确认子模式里用作"确认"，两者不冲突）。`f y d` = 收藏 · 克隆 · 删除。

## 不在本次范围

- 同面板多条消息的 FIFO 排队（现有一次 tick 全部投递；属于另一个问题，需在 `dispatchLocked` 前引入 per-pane 串行化）。
- session 的启停控制（只读名册）。
- 多用户 / 鉴权。

## 兼容性总结

- 存量消息文件不改写，空 `Session` = 主会话 = 今天行为。
- `herdr.Backend` 接口签名不变，`Fake` 只需补 session 字段。
- 极老 herdr（无 `api snapshot`）退回单会话，功能不中断。
