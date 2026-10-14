# Performance Characteristics

## The cost model (measured against Mail.app on macOS 26)

Every property read on a JXA object specifier is one Apple event round trip,
and Mail.app answers them serially. Measured costs:

| Operation | Cost |
|-----------|------|
| Launch `osascript` + connect to Mail | ~100 ms |
| One property read on one message (`msg.subject()`) | ~10 ms |
| Bulk property read over a mailbox (`mbox.messages.subject()`) | ~35 µs / message |
| `mbox.messages.length` | ~10 ms (small) – 200 ms (21k messages) |
| `mbox.messages()` (materialize all specifiers) | ~25 ms (small) – 700 ms (21k) |
| `mbox.messages.id()` (bulk ids) | ~400 ms on 21k messages |
| `mbox.messages.byId(n)` | direct specifier, effectively free |
| `mbox.messages.at(i)` | **re-enumerates the mailbox on every access** — never use in a loop |
| `mbox.messages.whose({...})` | ~0.8–1.5 s on 21k messages; bulk property reads on the result are pathologically slow (15 s for 16 messages) |
| `msg.content()` / `msg.properties()` | may block Mail.app's *entire* scripting interface until a body download completes — see below |

## How the CLI uses this

- **Message lookup by ID** uses `byId()`. IDs are unique across the whole Mail
  database, so no enumeration is needed; `messages.show`, `messages.mark`, `messages.flag`, `messages.quarantine`,
  `messages.archive`, `messages.move` and the attachment actions run in ~0.3 s
  regardless of mailbox size.
- **Batch mutations** (`messages.mark` with several ids) run in one `osascript`
  process: cost is one launch plus ~10 ms per message.
- **`messages.list`** is hybrid. Filters (`unread`, `flagged`, `since`)
  always use one bulk read per property. Then:
  - if the mailbox is small relative to the page (`total <= max(2000, page*300)`)
    every field is bulk-read — a 40-message INBOX lists in ~0.2 s;
  - otherwise only the ids are bulk-read and the page's messages are resolved
    with `byId()` and read individually — a 21k-message "All Mail" lists in ~2.5 s.
- **`mailboxes.list`** bulk-reads names and unread counts. `TotalCount` is only
  filled with `counts: true` because it needs a `messages.length` per mailbox
  (~3 s across 80 mailboxes vs ~0.7 s without).
- **`mailboxes.markRead`** uses `whose({readStatus: false})` and loops over the
  result; bulk assignment on a `whose()` specifier is not supported. It skips
  mailboxes whose `unreadCount` is already 0.
- Special mailboxes come from Mail's unified `trashMailbox`/`junkMailbox`/...
  properties (`.mailboxes()` gives the per-account boxes), so provider naming
  never matters.

## `withContent` can hang Mail.app

`msg.content()` (and `msg.properties()`, which includes it) makes Mail's main
thread wait synchronously on a MailCore worker in
`-[MCMessageBody attributedStringBlockingRemoteContent:...]`. If the body is
not cached locally and the fetch stalls, **all** AppleScript/JXA calls from
every process time out until Mail.app is restarted — the client going away does
not unblock it. Observed repeatedly on Gmail spam / "All Mail" messages right
after a Mail restart.

Mitigations: only request content for small, recently synced mailboxes; never
call `withContent` from unattended jobs against large or junk mailboxes; and
if `osascript -e "Application('Mail').accounts.length"` times out, sample Mail
(`sample Mail 1`) — a stack containing `MCMessage(ScriptingSupport) content`
means it must be restarted.

### Measured 2026-10-13 (this machine, warm cache): not reproduced

A scripted probe fetched `content()` from 7 messages on the Google (Gmail)
account — the largest in `Spam`, `All Mail` and `INBOX`, including one of
403 KB — with a per-call timeout and a liveness check after each one:

| Observation | Result |
|---|---|
| Trivial call (`accounts.length`) | ~135 ms, stable |
| First `content()` on a 403 KB message | 5.3 s, then 0.21 s for the same message via another mailbox |
| Gmail `Spam` newsletter (45 KB) | 1.77 s, 1,704 characters of body |
| Mail.app responsive after each fetch | yes, ~135–170 ms |
| Hangs | none in 7 fetches |

So the stall above is **not** a general property of `content()`: it needs the
cold-cache + remote-content case the paragraph describes (immediately after a
Mail.app start, body not downloaded, remote content to resolve). Treat the
mitigations as applying to that case. Reproducing it deliberately means quitting
and relaunching Mail.app first.

Also worth stating explicitly: `messages.search` never reads bodies. The search
implementation lowercases `subject()` and `sender()` and matches on those, so
"full-text search would hang" is an inference about reading every body in turn —
not something search does today.

### `source()` is the cheap way to the real URLs

`content()` returns the *rendered* body, in which `<a href="…">` has been
collapsed to its anchor text. `message.source()` returns the message as it
arrived, so the text/html part and the `List-Unsubscribe` headers survive:

| Call | Result on a 45 KB Gmail newsletter (id 7811) |
|---|---|
| `content()` | 1.77 s, 1,704 characters, no URLs |
| `source()` | **157 ms**, 44,840 characters, `multipart/alternative`, real unsubscribe URL present |

`source()` costs one read of the whole message (attachments included, so it
scales with the message rather than with the body), and it is still a body read,
so the warning above applies to it. The MIME parsing happens in Go
(`pkg/rawmail`), not in the scripting layer, which is what makes
`messages.show` with `body:"html"` cheap enough to offer at all.

## A mailbox created by scripting never receives mail (2026-10-14)

Moving messages into a mailbox that this tool created appeared to succeed and
changed nothing. Measured on the Exchange account, one message at a time:

| Destination | Result |
|---|---|
| `待删除`, created by scripting | reported `ok`, message never entered it, re-delivered to `收件箱` under a new id (8369 -> 8371 -> 8373 across attempts) |
| `存档`, pre-existing on the server | moved, and still there after a sync 60s later |

The id changing on every attempt is the signature: the server still has the
message in its original mailbox, so Mail.app re-inserts it and drops the local
copy. A scripted mailbox lives in Mail.app's local store only; the server has
never heard of it, so there is nothing to move the message into.

Consequences baked into the tool:

- `messages.quarantine` does not create the holding mailbox any more (it used to,
  which is how two empty local mailboxes ended up in the account). It fails with
  `HOLDING_BOX_UNAVAILABLE`, and the mailbox has to be created in Mail.app.
- `messages.move`, `messages.archive` and `messages.quarantine` read the
  destination back and report `confirmed` / `notPersisted`, tracked by RFC
  `Message-ID` because Mail.app assigns new ids on a move. Before this, 250
  messages were reported as changed while every one of them bounced back.
- `messages.archive` resolves the archive mailbox by name per account
  (`Archive`, `存档`, ...); it used to hard-code `Archive`, which never matches on
  a localized account.

A failed move is reported immediately: the message never appears in the
destination, so no sync round-trip is needed to detect it.

## Moves change message IDs

When a message is moved (archive, move, quarantine) Mail assigns it a new ID in the
destination mailbox. Re-list the destination if you need to act on it again.
