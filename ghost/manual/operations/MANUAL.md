# mail-app-cli 操作手册

这个插件把本机 macOS Mail.app 的邮箱封装成 Cindy 工具。真正的活儿由随包的
`mail-app-cli` 二进制完成：一份 JSON 文档进，一个结果信封出。工具面是两段式——
`list_tools` 给目录和规则，`call_tool` 执行。操作名就是 CLI 的 `action`，`args`
就是 CLI 的 `params` 对象。

前置条件：这台 Mac 上 Mail.app 已配置并能正常收发（本机账户：`Exchange`、`iCloud`、
`Google`）。第一次真正调用 Mail.app 时，macOS 会问一次「Cindy 想要控制"邮件"」，
允许即可；被拒绝时调用会失败，需要到「系统设置 → 隐私与安全性 → 自动化」里打开。

## 两条不变量

代码里做成了不可能，而不是不推荐：

| 做不到 | 改做什么 |
|--------|----------|
| **发信** | `draft` 只把信写进 Mail.app 的草稿箱就停下，由用户自己按发送 |
| **删信** | `messages.quarantine` —— 移进一个**用户自己在 Mail.app 里创建**的持有邮箱（默认 `待删除`），随时可在 Mail.app 里撤销 |

永远不要说"已发送"或"已删除"。用户要求删除时，做隔离，并说清这是隔离不是删除。

## 一次任务的顺序

1. **先便宜地看**：`mailboxes.list` 带 `unreadOnly:true` 是总览，顺带给出
   `unreadTotal`。
2. **再列邮件**：`messages.unified`（`box: inbox|unread|flagged|sent|drafts|trash|junk`）
   带 `since` + `format:"compact"`，一次覆盖所有账户。
3. **只看要紧的那几封**：`messages.show`。读正文是最贵的调用，不要滥用。
4. **要动就预演**：每个写操作都接受 `dryRun:true`。涉及多封时先把 dryRun 清单给用户
   看，再执行。`move` / `archive` / `quarantine` / 多封 `patch` / `markRead` 主机还会
   再弹一次确认框——用户点取消就什么都别做，也不要换个说法再问一遍。

## 结果信封与错误码

```json
{"ok": true, "okCount": 1, "failed": 0,
 "results": [{"index": 0, "action": "accounts.list", "ok": true, "data": {}}]}
```

每个 result 有 `ok`、`dryRun`、`data`、`error{code,hint}`。**按 `error.code` 分支**，
不要解析文案：

| 错误码 | 含义 |
|--------|------|
| `INVALID_REQUEST` | 文档、某个 params 对象或字段不对 |
| `UNKNOWN_ACTION` | 没有这个操作 |
| `ACCOUNT_NOT_FOUND` / `MAILBOX_NOT_FOUND` / `MESSAGE_MISSING` | 名字指向的东西不存在 |
| `PARTIAL_FAILURE` | 执行了，但不是每个 id 都成功；`data` 列出哪些没成 |
| `HOLDING_BOX_UNAVAILABLE` | 隔离用的持有邮箱不存在且建不出来 |
| `GMAIL_ARCHIVE_UNSUPPORTED` | Mail.app 不能安全归档 Gmail，改用 `messages.quarantine` |
| `MOVE_NOT_PERSISTED` | 移动报告成功但目标邮箱没收到——实际没移动 |
| `DRAFT_UNVERIFIED` | 草稿写了但 Drafts 里没看到，去 Mail.app 看看 |
| `MAIL_SCRIPT_ERROR` / `INTERNAL` | Mail.app 或工具本身失败 |

约定：**未知参数在任何嵌套层级都被拒绝**，错误会指出完整路径
（`params.message.in.account`）；拼错的字段永远不会静默回落到默认值。id 是 **JSON
字符串**（`"7847"`），不是数字。

## 共享参数对象

`list_tools(category=...)` 会把这些 defs 一并给出（只给该类别用到的），这里是速查：

- `in` = `{"account":"…","mailbox":"…"}`（都必填）。**省略 `in` 则全局解析 id**——
  Mail.app 的 id 全库唯一，所以一次调用可以混合多个账户。
- `message` = `{"id":"…","in":{…}}`。
- `set` = `{"read":true,"flagged":true}`；没写的字段不动。它取代了老的
  mark / flag 两个动作。
- `select`（`mailboxes.markRead`）= `{"mode":"mailbox","account":…,"mailbox":…}`
  **或** `{"mode":"special","kinds":["trash","junk","archive"],"accounts":[…]}`。
  两种模式混用直接解码错误。
- `content` = `{"subject":…,"body":…,"attach":[…]}`；`recipients` =
  `{"to":[…],"cc":[…],"bcc":[…]}`。

## 配方

```json
// 总览
{"action":"mailboxes.list","params":{"unreadOnly":true,"noCache":true}}
// 某个时间点之后到的信，一次覆盖所有账户
{"action":"messages.unified","params":{"box":"inbox","since":"2026-10-13","format":"compact"}}
// 一封完整的信
{"action":"messages.show","params":{"message":{"id":"7847","in":{"account":"Google","mailbox":"All Mail"}}}}
// 真实超链接 + 发件人自己的退订入口
{"action":"messages.show","params":{"message":{"id":"7811","in":{"account":"Google","mailbox":"Spam"}},"body":"links"}}
// 用隔离代替删除
{"action":"messages.quarantine","params":{"ids":["7844","7846"],"dryRun":true}}
// 一次调用改已读 + 旗标
{"action":"messages.patch","params":{"ids":["7847"],"set":{"read":true,"flagged":true}}}
// 清掉没人看的邮箱
{"action":"mailboxes.markRead","params":{"select":{"mode":"special","kinds":["trash","junk"]},"dryRun":true}}
// 起草一封等用户确认发送的回信
{"action":"draft","params":{"account":"Google","recipients":{"to":["a@b.com"]},
 "content":{"subject":"Re: 会议","body":"第一行\n第二行带 \"引号\""}}}
// 把某封邮件在 Mail.app 里交给用户
{"action":"open","params":{"message":{"id":"7847","in":{"account":"Google","mailbox":"All Mail"}}}}
```

## 坑表

| 坑 | 正确做法 |
|----|----------|
| 参数写在 `action` 旁边 | 参数在 `params` 里面；多余字段会被拒绝并给出提示 |
| 把 id 当数字读 | id 是 JSON **字符串**（`"7847"`） |
| 移动后继续用旧 id | `move`/`archive`/`quarantine` 会给邮件**新 id**——重新列目标邮箱 |
| 以为 `draft` 会发信 | 它不会。报告 `verified`，Mail.app 还没落库时报告 `syncing`；找不到则 `DRAFT_UNVERIFIED` |
| 想把草稿移出 Drafts | Mail.app 会立刻把它放回去并给新 id。只有用户能在 Mail.app 里删草稿 |
| 拿 Trash 当暂存区 | 各账户的 Trash 会按各自的时间表清空。要暂存就用 quarantine |
| 用本工具创建持有邮箱 | 脚本建的邮箱**只在本地**：服务端会把移进去的邮件重新投递并给新 id。请用户在 Mail.app 里创建，再隔离进去 |
| 相信 `move` 返回的 `changed: N` | 那不是证据。`move`/`archive`/`quarantine` 会回读目标并报告 `confirmed` / `notPersisted`；`notPersisted` 非空即 `MOVE_NOT_PERSISTED`，其实什么都没移动 |
| 以为邮箱名都是英文 | 这台机器是中文本地化的：`收件箱`、`草稿`、`垃圾邮件`、`已删除邮件`、`小道通讯`；Google 侧仍是英文（`INBOX`、`Drafts`、`Spam`、`Trash`、`All Mail`、`Important`） |
| 期待缓存给新鲜计数 | accounts/mailboxes 缓存 60 秒，邮件列表 5 分钟。在意新鲜度就传 `noCache:true` |
| 对大的或垃圾邮箱用 `withContent` | 它可能堵死 Mail.app 整个脚本接口，直到正文下载完成 |
| 想全文搜索正文 | `messages.search` 从不读正文，所以它便宜；全文搜索不提供。任何读正文的调用（`withContent`、`messages.show`）都是有风险的那个 |
| 报告"没有退订链接" | 默认正文是 Mail.app 的**渲染文本**，URL 已被压成锚文本。用 `body:"html"` 或 `"links"` 取到真实 href 与 `listUnsubscribe`（RFC 2369/8058，发件人自己的退订路由，有时正文里根本没有锚点也有它） |
| 让附件悄悄落到别处 | `attachments.save` 的 `output` 是文件系统路径；相对路径或省略会落到用户主目录。返回的 `saved` 是绝对路径，把它告诉用户 |

## 性能与缓存

每读一个属性都是一次 Apple 事件往返，所以客户端会批量读、按 id 解析。写新查询前先看仓库
里的 `PERFORMANCE.md`。突变会让它触及的缓存失效；重新列一次就能看到结果。

## 排障

- `list_tools` 的返回里出现 `runtime: "degraded"` 或 `warning`：说明包内二进制没跑起来。
  在插件源码目录重建并重新打包：
  `go build -o ghost/bin/mail-app-cli . && chmod 755 ghost/bin/mail-app-cli`
- `call_tool` 返回 `BINARY_MISSING` / `SPAWN_FAILED`：同上。
- `MAIL_SCRIPT_ERROR` 且提示权限：到「系统设置 → 隐私与安全性 → 自动化」允许 Cindy
  控制「邮件」。
- `CONFIRM_DENIED`：用户点了取消。不要重试，也不要换说法再弹一次。
- `CHILD_TIMEOUT`：Mail.app 卡住了（常见于正文下载）。重试一次；还不行就在 Mail.app
  里手动点一下那封邮件让它先下载完。
