---
name: mail-app-cli
description: Read and organize the user's real macOS Mail.app mailbox through the mail-app-cli JSON tool — accounts, mailboxes, unread mail, message bodies, attachments, search, read/flag state, archive, quarantine (never delete) and drafts (never send). Use when the user asks about their email, inbox, unread messages, a specific message or attachment, or wants a reply drafted for them to approve.
compatibility: macOS with Mail.app configured; needs the mail-app-cli binary on PATH and Automation permission for Mail.app.
---

# mail-app-cli

JSON in, JSON out. One binary, no dependencies, **no flag interface**. Every request is one document:

```bash
mail-app-cli '{"action":"messages.unified","params":{"box":"unread","limit":20,"format":"compact"}}'
```

## Install

Binary: `~/.local/bin/mail-app-cli`. Source of truth: `~/projects/mail-app-cli`.

```bash
cd ~/projects/mail-app-cli && go build -o ~/.local/bin/mail-app-cli .
mail-app-cli --version
```

The skill itself is maintained in that repo at `skill/mail-app-cli/`; copy it into place after changes:

```bash
rsync -a --delete ~/projects/mail-app-cli/skill/mail-app-cli/ ~/.agents/skills/mail-app-cli/
```

First run needs Automation permission for Mail.app (System Settings → Privacy & Security → Automation).

## Two invariants

The tool is built so the irreversible things are impossible, not discouraged:

| It cannot | Do this instead |
|-----------|-----------------|
| **send mail** | `draft` — composes into Mail.app's Drafts mailbox; the user reviews and presses Send |
| **delete mail** | `messages.quarantine` — moves into a holding mailbox (default `待删除`), reversible from Mail.app |

Never claim mail was sent or deleted. If the user asks to delete something, quarantine it and say that is what happened.

Every mutation takes `"dryRun": true`. **Dry-run anything that touches more than one message**, show the user the list, then apply.

## Do not guess parameters (IMPORTANT)

`schema` is generated from the same structs the decoder uses, so it is the source of truth:

```bash
mail-app-cli '{"action":"schema"}' | jq -r '.results[0].data.actions[] | "\(.name)  \(.summary)"'
mail-app-cli '{"action":"schema"}' | jq '.results[0].data.actions[] | select(.name=="messages.quarantine")'
mail-app-cli '{"action":"schema"}' | jq '.results[0].data.defs'      # shared parameter objects
```

This matters because unknown parameters are **rejected, never ignored**, at any depth, and the error names the path (`params.message.in.account`).

## Reading the answer

- Envelope: `{"ok", "okCount", "failed", "results":[...]}`; each result has `ok`, `dryRun`, `data`, `error{code,hint}`.
- **Exit code** is `0` when every operation succeeded, `1` otherwise — the JSON is printed either way.
- Branch on `error.code`, not on prose: `MESSAGE_MISSING`, `MAILBOX_NOT_FOUND`, `ACCOUNT_NOT_FOUND`, `PARTIAL_FAILURE` (data still lists what worked), `HOLDING_BOX_UNAVAILABLE`, `GMAIL_ARCHIVE_UNSUPPORTED`, `DRAFT_UNVERIFIED`, `INVALID_REQUEST`, `UNKNOWN_ACTION`.
- Add `"pretty": true` (next to `"action"`) only when a human will read it; the default single line is about half the bytes.
- `"format": "compact"` on reads returns 8 fields per message instead of 16 — use it for anything you will scan.

## Working order

1. `mailboxes.list` with `unreadOnly: true` — cheapest overview, also reports `unreadTotal`.
2. `messages.unified` (`box: inbox|unread|flagged|sent|drafts|trash|junk`) with `since` + `format: compact` — one call covers every account.
3. `messages.show` only for the messages that matter: bodies are the expensive read.
4. `dryRun` the changes → tell the user → apply.

## Read actions

| Action | Required | Optional |
|--------|----------|----------|
| `accounts.list` | — | `noCache` |
| `mailboxes.list` | — | `account`, `counts`, `unreadOnly`, `noCache` |
| `messages.list` | `account`, `mailbox` | `unread`, `flagged`, `since`, `limit`, `offset`, `withContent`, `withHeaders`, `noCache`, `format` |
| `messages.unified` | `box` | `since`, `limit`, `offset`, `withContent`, `withHeaders`, `format` |
| `messages.show` | `message` | — |
| `messages.search` | `query` | `account`, `mailbox`, `limit`, `format` |
| `messages.resolve` | `ids` | — |
| `attachments.list` | `message` | — |
| `attachments.save` | `message`, `name` | `output` |

`since` is `YYYY-MM-DD` or `YYYY-MM-DD HH:MM:SS`. `messages.search` matches subject and sender only — bodies are not searched. `messages.resolve` reports where ids live and changes nothing.

## Write actions

| Action | Required | Optional |
|--------|----------|----------|
| `messages.patch` | `ids`, `set` | `in`, `dryRun` |
| `messages.move` | `ids`, `to` | `in`, `dryRun` |
| `messages.archive` | `ids` | `in`, `gmailPolicy`, `dryRun` |
| `messages.quarantine` | `ids` | `box`, `in`, `dryRun` |
| `mailboxes.markRead` | `select` | `unread`, `dryRun` |
| `draft` | `recipients`, `content` | `account`, `from`, `dryRun` |
| `sync` | — | `account` |
| `open` | — | `message` |

Shared objects (see `schema` → `defs`) — learn these once:

- `in` is `{"account":"…","mailbox":"…"}` (both required). **Omit it to resolve ids globally**; Mail.app ids are unique database-wide, so one call can mix accounts.
- `message` is `{"id":"…","in":{…}}`.
- `set` is `{"read":true,"flagged":true}` — a field left out is left untouched; it replaces the old separate mark and flag calls.
- `select` is a union: `{"mode":"mailbox","account":…,"mailbox":…}` **or** `{"mode":"special","kinds":["trash","junk","archive"],"accounts":[…]}`. Mixing the two modes is a decode error.
- `content` is `{"subject":…,"body":…,"attach":[…]}`; `recipients` is `{"to":[…],"cc":[…],"bcc":[…]}`.

## Recipes

```bash
# overview
mail-app-cli '{"action":"mailboxes.list","params":{"unreadOnly":true,"noCache":true}}' \
  | jq -r '.results[0].data.mailboxes[] | "\(.Account)/\(.Name): \(.UnreadCount)"'

# what arrived since a point in time, every account at once
mail-app-cli '{"action":"messages.unified","params":{"box":"inbox","since":"2026-10-13","format":"compact"}}' \
  | jq -r '.results[0].data.messages[] | "\(.date[0:10])  \(.from)  —  \(.subject)"'

# one message in full
mail-app-cli '{"action":"messages.show","params":{"message":{"id":"7847","in":{"account":"Google","mailbox":"All Mail"}}}}'

# propose, then apply (quarantine is the safe "get it out of the inbox")
mail-app-cli '{"action":"messages.quarantine","params":{"ids":["7844","7846"],"dryRun":true}}'
mail-app-cli '{"action":"messages.quarantine","params":{"ids":["7844","7846"]}}'

# read + flagged in one call
mail-app-cli '{"action":"messages.patch","params":{"ids":["7847"],"set":{"read":true,"flagged":true}}}'

# clear the mailboxes nobody reads
mail-app-cli '{"action":"mailboxes.markRead","params":{"select":{"mode":"special","kinds":["trash","junk"]},"dryRun":true}}'

# draft a reply for the user to send (the body needs no shell escaping)
mail-app-cli <<'JSON'
{"action":"draft","params":{"account":"Google",
 "recipients":{"to":["someone@example.com"]},
 "content":{"subject":"Re: 会议","body":"第一行\n第二行带 \"引号\""}}}
JSON

# hand a message to the user in Mail.app
mail-app-cli '{"action":"open","params":{"message":{"id":"7847","in":{"account":"Google","mailbox":"All Mail"}}}}'
```

Batch several operations (one process, and one failure does not stop the rest):

```bash
mail-app-cli <<'JSON'
{"ops":[
  {"action":"messages.unified","params":{"box":"unread","limit":10,"format":"compact"}},
  {"action":"messages.quarantine","params":{"ids":["7844"],"dryRun":true}}
]}
JSON
```

## Pitfalls

| Pitfall | Correct approach |
|---------|-----------------|
| Parameters next to `"action"` | They go inside `"params"`; a stray field is refused with a hint |
| Reading ids as numbers | Ids are **strings** in JSON (`"7847"`) |
| Acting on an id after a move | `move`/`archive`/`quarantine` give the message a **new id** — re-list the destination |
| Expecting `draft` to send | It cannot. It reports `verified`, or `syncing` when Mail.app has not stored it yet; not found → `DRAFT_UNVERIFIED` |
| Trying to move a draft out of Drafts | Mail.app puts it straight back under a new id. Only the user can remove drafts, in Mail.app |
| Using Trash as a holding area | Mail.app empties each account's Trash on its own schedule. Quarantine instead |
| Assuming English mailbox names | This machine is localized: `收件箱`, `草稿`, `垃圾邮件`, `已删除邮件`, `小道通讯`; Google stays English (`INBOX`, `Drafts`, `Spam`, `Trash`, `All Mail`, `Important`) |
| Expecting fresh counts from cache | `accounts`/`mailboxes` cache 60s, message lists 5 min. Pass `noCache: true` when freshness matters |
| `withContent` on a big or spam mailbox | It can block Mail.app's whole scripting interface until a body downloads |
| Searching for body text | `messages.search` only looks at subject and sender — use `withContent` on a small mailbox instead |

Accounts on this machine: `Exchange`, `iCloud`, `Google`.

## Notes

- Mail.app must be running; the tool drives it through `osascript` (JXA/AppleScript).
- Every property read is one Apple event round trip, so the client bulk-reads and resolves by id — read `PERFORMANCE.md` in the repo before adding new queries.
- Mutations invalidate the caches they touch; just re-list to see the result.
- Tests cover decoding, validation, the schema and the dry-run paths only (`go test ./...`); anything that writes to the mailbox is verified by hand, on purpose.
