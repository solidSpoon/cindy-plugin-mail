<p align="center">
  <img src="logo.png" alt="mail-app-cli logo" width="200">
</p>

# mail-app-cli

**An [Intelligrit Labs](https://intelligrit.com#labs) Project**

A JSON-driven interface to macOS Mail.app. One request document in, one result
envelope out — accounts, mailboxes, messages, attachments and drafts, scriptable
from any language that can spawn a process and write JSON.

```bash
mail-app-cli '{"action":"messages.unified","params":{"box":"unread","limit":5,"format":"compact"}}'
```

## Two invariants

The tool is built so that the dangerous things are not possible, not merely
discouraged. Both are reported by the `schema` action at runtime.

- **It cannot send mail.** `draft` composes a message into Mail.app's Drafts
  mailbox and stops there; you review it and press Send in Mail.app.
- **It cannot delete mail.** There is no delete action. `messages.quarantine`
  moves messages into a holding mailbox (default `待删除`, created per account on
  demand), which stays visible and reversible from Mail.app.

Every mutation also accepts `"dryRun": true`, which reports exactly what it
would touch and changes nothing.

## Install

```bash
go build -o mail-app-cli .     # or: make build / go install
```

Requires macOS with Mail.app configured. The first run asks for permission to
control Mail.app (System Settings → Privacy & Security → Automation).

## The protocol

An operation is an action plus **its own parameter object**. The two are kept in
separate layers, so the parameters of an action can be structured (grouped,
nested, shared between actions) instead of every action pouring its fields into
one flat namespace.

```json
{
  "action": "messages.show",
  "params": {
    "message": { "id": "7847", "in": { "account": "Google", "mailbox": "All Mail" } }
  }
}
```

Input is a single JSON document, passed as the first argument or on stdin. It is
either one operation or a batch:

```bash
# one operation
mail-app-cli '{"action":"accounts.list"}'

# a batch: every operation runs, and one failure does not stop the others
mail-app-cli <<'JSON'
{
  "ops": [
    {"action": "messages.unified", "params": {"box": "unread", "limit": 10, "format": "compact"}},
    {"action": "messages.quarantine", "params": {"ids": ["7844"], "dryRun": true}}
  ]
}
JSON
```

The answer is always a JSON envelope on stdout:

```json
{
  "ok": true,
  "okCount": 1,
  "failed": 0,
  "results": [
    {"index": 0, "action": "accounts.list", "ok": true,
     "data": {"count": 3, "accounts": [...]}}
  ]
}
```

Failures carry a stable machine-readable code, so a caller branches on the code
instead of parsing prose:

```json
{"index": 0, "action": "messages.quarantine", "ok": false,
 "error": {"code": "MESSAGE_MISSING", "message": "no message 7844",
           "hint": "re-list the mailbox first: mutations assign new ids"}}
```

| Code | Meaning |
|------|---------|
| `INVALID_REQUEST` | The document, an action's params object, or a field is wrong |
| `UNKNOWN_ACTION` | No such action |
| `ACCOUNT_NOT_FOUND`, `MAILBOX_NOT_FOUND`, `MESSAGE_MISSING` | The named thing does not exist |
| `PARTIAL_FAILURE` | The action ran, but not every id was handled — `data` lists which |
| `HOLDING_BOX_UNAVAILABLE` | The quarantine mailbox is missing and could not be created |
| `GMAIL_ARCHIVE_UNSUPPORTED` | Mail.app cannot archive Gmail; use `messages.quarantine` |
| `DRAFT_UNVERIFIED` | The draft was composed but not seen in Drafts; look in Mail.app |
| `MAIL_SCRIPT_ERROR`, `INTERNAL` | Mail.app or the tool failed |

Conventions:

- **Unknown parameters are rejected**, at any nesting depth, and the error names
  the full path (`params.message.in.account`). A misspelled field never silently
  falls back to a default.
- **Exit code** is `0` when every operation succeeded, `1` otherwise. The
  envelope is printed either way.
- **Output is single-line JSON** by default. Add `"pretty": true` next to
  `"action"` to indent it (indentation roughly doubles a list result).
- **Reads** accept `"format": "compact"`, which keeps only the fields a triage
  decision needs (`id`, `account`, `mailbox`, `date`, `read`, `flagged`, `from`,
  `subject`) and typically halves the payload.
- **Multiple ids go in one call.** Omitting `in` resolves ids globally, because
  Mail.app message ids are unique database-wide.
- Output is JSON-safe UTF-8 with HTML escaping off, so addresses read as
  `Name <a@b.com>` rather than `Name \u003ca@b.com\u003e`.

### Shared parameter objects

Grouping parameters makes the repeating ones definable once. `{"account": …,
"mailbox": …}` is `MailboxRef`, and every action that needs a mailbox refers to
it, so there is one place to learn it and one place it can be wrong:

| Object | Fields | Used by |
|--------|--------|---------|
| `MailboxRef` | `account`, `mailbox` (both required) | `messages.list`, `messages.patch`, `messages.move`, `messages.archive`, `messages.quarantine`, `open` |
| `MessageInMailbox` | `id`, `in` (`MailboxRef`) | `messages.show`, `attachments.list`, `attachments.save` |
| `Recipients` | `to` (required), `cc`, `bcc` | `draft` |
| `StateSet` | `read`, `flagged` (at least one) | `messages.patch` |
| `MarkSelect` | `mode`, then the fields for that mode | `mailboxes.markRead` |

### Discover the interface at runtime

```bash
mail-app-cli '{"action":"schema"}'
```

returns every action with its params object, plus a `defs` section for the shared
objects above (with `ref` pointing at them). It is generated by reflection over
the same structs the decoder uses, so it cannot drift from the implementation.

## Actions

### Read

| Action | Params |
|--------|--------|
| `schema` | — |
| `accounts.list` | `noCache` |
| `mailboxes.list` | `account`, `counts`, `unreadOnly`, `noCache` |
| `messages.list` | `account`, `mailbox`, `unread`, `flagged`, `since`, `limit`, `offset`, `withContent`, `withHeaders`, `noCache`, `format` |
| `messages.unified` | `box` (`inbox\|unread\|flagged\|sent\|drafts\|trash\|junk`), `since`, `limit`, `offset`, `withContent`, `withHeaders`, `format` |
| `messages.show` | `message` (`MessageInMailbox`) |
| `messages.search` | `query`, `account`, `mailbox`, `limit`, `format` |
| `messages.resolve` | `ids` |
| `attachments.list` | `message` |
| `attachments.save` | `message`, `name`, `output` |

`messages.unified` reads across all accounts in one call, and `since` accepts
`YYYY-MM-DD` or `YYYY-MM-DD HH:MM:SS`. `messages.search` matches subject and
sender only, and it never reads message bodies: reading a body
(`withContent`, or `messages.show`) is the one call that can block Mail.app's
entire scripting interface while a download stalls, so a full-text search is not
offered. See [PERFORMANCE.md](PERFORMANCE.md).
`messages.resolve` reports where ids currently live and changes nothing, which is
what the `dryRun` paths use.

### Write

| Action | Params |
|--------|--------|
| `messages.patch` | `ids`, `set` (`StateSet`), `in`, `dryRun` |
| `messages.move` | `ids`, `to`, `in`, `dryRun` |
| `messages.archive` | `ids`, `in`, `gmailPolicy` (`skip\|read`), `dryRun` |
| `messages.quarantine` | `ids`, `box`, `in`, `dryRun` |
| `mailboxes.markRead` | `select` (`MarkSelect`), `unread`, `dryRun` |
| `draft` | `account`, `from`, `recipients` (`Recipients`), `content`, `dryRun` |
| `attachments.save` | `message`, `name`, `output` |
| `sync` | `account` |
| `open` | `message` (`{in?, id?}`) |

Notes that are easy to get wrong:

- **Identifiers are always quoted strings**, because a Mail.app id is an integer
  that JSON callers would otherwise lose in float formatting.
- **A move changes the id.** Mail.app assigns the message a new id in the
  destination mailbox, so re-list after `move`, `archive` or `quarantine`.
- **`draft` never sends.** It reports the draft's `id` and `verified`. If Mail.app
  has not stored it yet the result says `syncing`, and if it is not found the
  action fails with `DRAFT_UNVERIFIED` rather than claiming success.
- **Quarantine, don't delete.** For Gmail that is a label change, and for every
  account it survives Mail.app's own `empty trash frequency` (which is why the
  account's Trash is not a safe holding area).
- **`withContent` and `content.attach` are the slow ones.** See
  [PERFORMANCE.md](PERFORMANCE.md) for the measured cost model of everything
  else.

## Recipes

Unread overview, cheapest first:

```bash
mail-app-cli '{"action":"mailboxes.list","params":{"unreadOnly":true}}' \
  | jq -r '.results[0].data.mailboxes[] | "\(.Account)/\(.Name): \(.UnreadCount)"'
```

What arrived since a point in time, across every account:

```bash
mail-app-cli '{"action":"messages.unified",
               "params":{"box":"inbox","since":"2026-10-13","format":"compact"}}' \
  | jq -r '.results[0].data.messages[] | "\(.date[0:10])  \(.from)  —  \(.subject)"'
```

Read one message in full:

```bash
mail-app-cli '{"action":"messages.show",
               "params":{"message":{"id":"7847",
                                    "in":{"account":"Google","mailbox":"All Mail"}}}}'
```

Propose changes, then apply them:

```bash
mail-app-cli '{"action":"messages.quarantine","params":{"ids":["7844","7846"],"dryRun":true}}'
mail-app-cli '{"action":"messages.quarantine","params":{"ids":["7844","7846"]}}'
```

Mark read and flag in one call:

```bash
mail-app-cli '{"action":"messages.patch",
               "params":{"ids":["7847","7846"],"set":{"read":true,"flagged":true},
                         "in":{"account":"Exchange","mailbox":"收件箱"}}}'
```

Mark the things nobody reads anyway, without moving anything:

```bash
mail-app-cli '{"action":"mailboxes.markRead",
               "params":{"select":{"mode":"special","kinds":["trash","junk"]},"dryRun":true}}'
```

Compose a draft for a human to send (the body needs no shell escaping):

```bash
mail-app-cli <<'JSON'
{
  "action": "draft",
  "params": {
    "account": "Google",
    "recipients": {"to": ["someone@example.com"]},
    "content": {
      "subject": "会议纪要",
      "body": "第一行\n第二行带 \"引号\" 和 $变量\n第三行"
    }
  }
}
JSON
```

Hand a message to the user in Mail.app:

```bash
mail-app-cli '{"action":"open",
               "params":{"message":{"id":"7847",
                                    "in":{"account":"Google","mailbox":"All Mail"}}}}'
```

## Agent skill

The agent-facing instructions for this tool live in the repo at
[`skills/mail-app-cli/SKILL.md`](skills/mail-app-cli/SKILL.md). They are maintained
here and copied into the agent's global skills directory, so the repo stays the
source of truth:

```bash
rsync -a --delete skills/mail-app-cli/ ~/.agents/skills/mail-app-cli/
```

(`~/.agents/skills/` is Pi's global skill location; a folder there whose name
matches the skill's `name` is enough for it to be discovered.)

## How it works

Mail.app is driven through `osascript` (JavaScript for Automation and
AppleScript). Every property read on a Mail.app object is one Apple event round
trip and Mail.app answers them serially, which is why the client bulk-reads
properties, resolves messages by id instead of by index, and batches mutations
into a single call. [PERFORMANCE.md](PERFORMANCE.md) records the measured cost
of each idiom and which ones were abandoned.

Reads are cached briefly (`accounts`/`mailboxes` 60s, message lists 5 minutes per
query); `noCache: true` bypasses it and mutations invalidate what they touch.

## Project layout

```
main.go              transport: read the document, print the envelope, set the exit code
pkg/api/             the JSON surface: params structs, schema, dispatch, handlers
  params.go          the shared parameter objects (MailboxRef, Recipients, ...)
  schema.go          reflection over the params structs -> defs + actions
  registry.go        action registry, params decoding, validation
pkg/mail/            the Mail.app engine (AppleScript / JXA)
pkg/cache/           the small on-disk cache
```

`go test ./...` covers request decoding, nested and union validation, the schema
and the dry-run paths. Anything that would need a real mailbox is exercised
manually instead.

## JSON envelope reference

```jsonc
{
  "ok": true,        // every operation succeeded
  "okCount": 1,      // operations that succeeded
  "failed": 0,       // operations that did not
  "results": [
    {
      "index": 0,          // position in ops, or 0 for a single operation
      "action": "accounts.list",
      "ok": true,
      "dryRun": false,     // true when the operation only reported
      "data": { },         // action-specific payload
      "error": {           // present when ok is false
        "code": "MESSAGE_MISSING",
        "message": "human and model readable",
        "hint": "what to do next"
      }
    }
  ],
  "error": { }       // set only when the document itself could not be handled
}
```

## License

MIT — see [LICENSE](LICENSE).
