package api

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/intelligrit/mail-app-cli/pkg/cache"
	"github.com/intelligrit/mail-app-cli/pkg/mail"
	"github.com/intelligrit/mail-app-cli/pkg/rawmail"
)

// Cache lifetimes. Accounts and mailboxes used to be cached for 24h, which made
// a scheduled run report stale unread counts with no indication that it had;
// they are short-lived now so a fresh invocation is never more than a minute
// behind Mail.app. Message lists are keyed by every query parameter.
const (
	accountsCacheTTL  = 60 * time.Second
	mailboxesCacheTTL = 60 * time.Second
	messagesCacheTTL  = 5 * time.Minute
)

func sanitizeCacheKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// invalidateMailboxCache drops the cached message lists of one mailbox (of
// every mailbox in an account when mailbox is empty, or all mailboxes when
// account is empty).
func invalidateMailboxCache(account, mailbox string) {
	c, err := cache.New()
	if err != nil {
		return
	}
	prefix := "msgs-"
	if account != "" {
		prefix = fmt.Sprintf("msgs-%s-", sanitizeCacheKey(account))
		if mailbox != "" {
			prefix = fmt.Sprintf("msgs-%s-%s-", sanitizeCacheKey(account), sanitizeCacheKey(mailbox))
		}
	}
	c.DeletePrefix(prefix)
}

func invalidateMailboxListCache() {
	if c, err := cache.New(); err == nil {
		c.DeletePrefix("mailboxes")
	}
}

// ---------------------------------------------------------------- schema

type SchemaParams struct{}

func init() {
	Register("schema", "Describe every action, its parameter object and the tool's constraints", func(SchemaParams) (any, error) {
		return schemaData(), nil
	})
}

// -------------------------------------------------------------- accounts

type AccountsListParams struct {
	NoCache bool `json:"noCache,omitempty" desc:"bypass the cache and read Mail.app now"`
}

func init() {
	Register("accounts.list", "List the Mail.app accounts", func(req AccountsListParams) (any, error) {
		var accounts []mail.Account
		c, cacheErr := cache.New()
		if !req.NoCache && cacheErr == nil {
			c.SetTTL(accountsCacheTTL)
			if found, err := c.Get("accounts", &accounts); err == nil && found {
				return map[string]any{"accounts": accounts, "count": len(accounts), "cached": true}, nil
			}
		}
		accounts, err := mail.NewClient().GetAccountsJSON()
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to read accounts: %v", err)
		}
		if cacheErr == nil {
			c.SetTTL(accountsCacheTTL)
			_ = c.Set("accounts", accounts)
		}
		return map[string]any{"accounts": accounts, "count": len(accounts), "cached": false}, nil
	})
}

// -------------------------------------------------------------- mailboxes

type MailboxesListParams struct {
	Account    string `json:"account,omitempty" desc:"limit to one account"`
	Counts     bool   `json:"counts,omitempty" desc:"also read per-mailbox TotalCount (slower: enumerates each mailbox)"`
	UnreadOnly bool   `json:"unreadOnly,omitempty" desc:"only mailboxes that currently have unread mail"`
	NoCache    bool   `json:"noCache,omitempty" desc:"bypass the cache and read Mail.app now"`
}

func init() {
	Register("mailboxes.list", "List mailboxes with unread counts", func(req MailboxesListParams) (any, error) {
		cacheKey := "mailboxes"
		if req.Account != "" {
			cacheKey = "mailboxes-" + sanitizeCacheKey(req.Account)
		}
		if req.Counts {
			cacheKey += "-counts"
		}

		var (
			mailboxes []mail.Mailbox
			err       error
		)
		c, cacheErr := cache.New()
		cached := false
		if !req.NoCache && cacheErr == nil {
			c.SetTTL(mailboxesCacheTTL)
			if found, gerr := c.Get(cacheKey, &mailboxes); gerr == nil && found {
				cached = true
			}
		}
		if !cached {
			mailboxes, err = mail.NewClient().GetMailboxesJSON(req.Account, req.Counts)
			if err != nil {
				return nil, errf(CodeScriptFailed, "failed to read mailboxes: %v", err)
			}
			if cacheErr == nil {
				c.SetTTL(mailboxesCacheTTL)
				_ = c.Set(cacheKey, mailboxes)
			}
		}

		unreadTotal := 0
		for _, mb := range mailboxes {
			unreadTotal += mb.UnreadCount
		}
		if req.UnreadOnly {
			only := make([]mail.Mailbox, 0, len(mailboxes))
			for _, mb := range mailboxes {
				if mb.UnreadCount > 0 {
					only = append(only, mb)
				}
			}
			mailboxes = only
		}
		return map[string]any{
			"mailboxes":   mailboxes,
			"count":       len(mailboxes),
			"unreadTotal": unreadTotal,
			"cached":      cached,
		}, nil
	})
}

// --------------------------------------------------------------- messages

// compactMessage is the triage projection of a message: everything needed to
// decide what to do with it, and nothing that is empty for the query anyway.
type compactMessage struct {
	ID      string `json:"id"`
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
	Date    string `json:"date"`
	Read    bool   `json:"read"`
	Flagged bool   `json:"flagged"`
	From    string `json:"from"`
	Subject string `json:"subject"`
}

func projectMessages(messages []mail.Message, full bool) any {
	if full {
		return messages
	}
	out := make([]compactMessage, 0, len(messages))
	for _, m := range messages {
		date := m.DateReceived
		if date == "" {
			date = m.DateSent
		}
		out = append(out, compactMessage{
			ID: m.ID, Account: m.Account, Mailbox: m.Mailbox, Date: date,
			Read: m.Read, Flagged: m.Flagged, From: m.Sender, Subject: m.Subject,
		})
	}
	return out
}

const (
	defaultListLimit   = 60
	defaultDigestLimit = 200
)

// MessagesListParams reads messages from one named mailbox inside one account.
type MessagesListParams struct {
	In          MailboxRef `json:"in" req:"1" desc:"the mailbox to list"`
	Unread      bool       `json:"unread,omitempty" desc:"only unread messages"`
	Flagged     bool       `json:"flagged,omitempty" desc:"only flagged messages"`
	Since       string     `json:"since,omitempty" desc:"only messages received at or after this date: YYYY-MM-DD or YYYY-MM-DD HH:MM:SS"`
	Query       string     `json:"query,omitempty" desc:"case-insensitive substring match on subject and sender (local metadata filter, never reads bodies)"`
	Limit       int        `json:"limit,omitempty" def:"60" desc:"maximum messages to return"`
	Offset      int        `json:"offset,omitempty" desc:"messages to skip (pagination)"`
	WithContent bool       `json:"withContent,omitempty" desc:"include bodies (switches to full message shape; can block Mail.app while a body downloads)"`
	WithHeaders bool       `json:"withHeaders,omitempty" desc:"include InReplyTo/References for threading (switches to full message shape; about 9x slower)"`
	NoCache     bool       `json:"noCache,omitempty" desc:"bypass the short-lived list cache"`
}

func filterMessagesByQuery(messages []mail.Message, query string) []mail.Message {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return messages
	}
	out := make([]mail.Message, 0, len(messages))
	for _, m := range messages {
		if strings.Contains(strings.ToLower(m.Subject), q) ||
			strings.Contains(strings.ToLower(m.Sender), q) {
			out = append(out, m)
		}
	}
	return out
}

func paginateMessages(messages []mail.Message, offset, limit int) ([]mail.Message, bool) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(messages) {
		return []mail.Message{}, false
	}
	rest := messages[offset:]
	if limit > 0 && len(rest) > limit {
		return rest[:limit], true
	}
	return rest, false
}

func init() {
	Register("messages.list", "List messages in one mailbox (compact triage fields by default)", func(req MessagesListParams) (any, error) {
		if req.Limit <= 0 {
			req.Limit = defaultListLimit
		}
		account, mailbox := req.In.Account, req.In.Mailbox
		cacheKey := fmt.Sprintf("msgs-%s-%s-%d-%d-%v-%v-%s-%s-%v-%v",
			sanitizeCacheKey(account), sanitizeCacheKey(mailbox),
			req.Limit, req.Offset, req.Unread, req.Flagged,
			sanitizeCacheKey(req.Since), sanitizeCacheKey(req.Query), req.WithContent, req.WithHeaders)

		type cachedPage struct {
			Messages []mail.Message `json:"messages"`
			HasMore  bool           `json:"hasMore"`
		}
		var (
			messages []mail.Message
			hasMore  bool
			err      error
		)
		c, cacheErr := cache.New()
		cached := false
		if !req.NoCache && cacheErr == nil && !req.WithContent {
			c.SetTTL(messagesCacheTTL)
			var page cachedPage
			if found, gerr := c.Get(cacheKey, &page); gerr == nil && found {
				messages = page.Messages
				hasMore = page.HasMore
				cached = true
			}
		}
		if !cached {
			fetchLimit, fetchOffset := req.Limit, req.Offset
			if req.Query != "" {
				// Filter in Go after fetching metadata so offset/limit apply to
				// the matched subset rather than before matching.
				fetchLimit, fetchOffset = 0, 0
			} else if !req.WithContent && req.Limit > 0 {
				// Fetch one extra metadata row so we can report hasMore without
				// a second JXA round trip; skipped when withContent is set so
				// we never trigger an extra body download.
				fetchLimit = req.Limit + 1
			}
			messages, err = mail.NewClient().GetMessagesJSON(account, mailbox,
				fetchLimit, fetchOffset, req.Unread, req.Flagged, req.WithContent, req.WithHeaders, req.Since)
			if err != nil {
				return nil, errf(CodeScriptFailed, "failed to list %s/%s: %v", account, mailbox, err)
			}
			if req.Query != "" {
				messages = filterMessagesByQuery(messages, req.Query)
				messages, hasMore = paginateMessages(messages, req.Offset, req.Limit)
			} else if !req.WithContent && req.Limit > 0 && len(messages) > req.Limit {
				hasMore = true
				messages = messages[:req.Limit]
			}
			if cacheErr == nil && !req.NoCache && !req.WithContent {
				c.SetTTL(messagesCacheTTL)
				_ = c.Set(cacheKey, cachedPage{Messages: messages, HasMore: hasMore})
			}
		}
		out := map[string]any{
			"account": account, "mailbox": mailbox,
			"count": len(messages), "cached": cached,
			"messages": projectMessages(messages, req.WithContent || req.WithHeaders),
		}
		if !req.WithContent || req.Query != "" {
			out["hasMore"] = hasMore
			if hasMore {
				out["nextOffset"] = req.Offset + len(messages)
			}
		}
		return out, nil
	})
}

type MessagesDigestParams struct {
	In      MailboxRef `json:"in" req:"1" desc:"the mailbox to summarize"`
	Unread  bool       `json:"unread,omitempty" desc:"only unread messages"`
	Flagged bool       `json:"flagged,omitempty" desc:"only flagged messages"`
	Since   string     `json:"since,omitempty" desc:"only messages received at or after this date: YYYY-MM-DD or YYYY-MM-DD HH:MM:SS"`
	Query   string     `json:"query,omitempty" desc:"case-insensitive substring match on subject and sender before grouping"`
	Limit   int        `json:"limit,omitempty" def:"200" desc:"maximum messages to scan for the digest"`
}

type digestGroup struct {
	From        string   `json:"from"`
	Count       int      `json:"count"`
	UnreadCount int      `json:"unreadCount"`
	Newest      string   `json:"newest"`
	Subjects    []string `json:"subjects"`
	IDs         []string `json:"ids"`
}

func buildDigest(messages []mail.Message) []digestGroup {
	type accGroup struct {
		group       digestGroup
		seenSubject map[string]bool
	}
	bySender := map[string]*accGroup{}
	var order []string

	for _, m := range messages {
		displayFrom := strings.TrimSpace(m.Sender)
		if displayFrom == "" {
			displayFrom = "(unknown)"
		}
		key := strings.ToLower(displayFrom)
		ag, ok := bySender[key]
		if !ok {
			ag = &accGroup{
				group: digestGroup{
					From:     displayFrom,
					Subjects: []string{},
					IDs:      []string{},
				},
				seenSubject: map[string]bool{},
			}
			bySender[key] = ag
			order = append(order, key)
		}
		ag.group.Count++
		if !m.Read {
			ag.group.UnreadCount++
		}
		date := m.DateReceived
		if date == "" {
			date = m.DateSent
		}
		if date > ag.group.Newest {
			ag.group.Newest = date
		}
		if subj := strings.TrimSpace(m.Subject); subj != "" && len(ag.group.Subjects) < 3 && !ag.seenSubject[subj] {
			ag.seenSubject[subj] = true
			ag.group.Subjects = append(ag.group.Subjects, subj)
		}
		ag.group.IDs = append(ag.group.IDs, m.ID)
	}

	out := make([]digestGroup, 0, len(order))
	for _, key := range order {
		out = append(out, bySender[key].group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Newest != out[j].Newest {
			return out[i].Newest > out[j].Newest
		}
		return out[i].From < out[j].From
	})
	return out
}

func init() {
	Register("messages.digest", "Group message metadata in one mailbox by sender for low-token triage and bulk quarantine", func(req MessagesDigestParams) (any, error) {
		if req.Limit <= 0 {
			req.Limit = defaultDigestLimit
		}
		account, mailbox := req.In.Account, req.In.Mailbox
		messages, err := mail.NewClient().GetMessagesJSON(account, mailbox,
			req.Limit, 0, req.Unread, req.Flagged, false, false, req.Since)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to list %s/%s: %v", account, mailbox, err)
		}
		messages = filterMessagesByQuery(messages, req.Query)
		groups := buildDigest(messages)
		return map[string]any{
			"account": account,
			"mailbox": mailbox,
			"scanned": len(messages),
			"groups":  len(groups),
			"digest":  groups,
		}, nil
	})
}

type MessagesShowParams struct {
	ID   string     `json:"id" req:"1" desc:"Mail.app message id"`
	In   MailboxRef `json:"in" req:"1" desc:"the mailbox the message is currently in"`
	Body string     `json:"body,omitempty" enum:"text|html|links" def:"text" desc:"text: Mail.app's rendered body, in which hyperlinks are already flattened to their anchor text. html: parse the raw message source and return its html, links and listUnsubscribe. links: the same parse without the html field, when all you need are the addresses."`
}

func init() {
	Register("messages.show", "Show one message in full, including its body and thread headers. body:\"html\" or \"links\" recovers the hyperlinks and the List-Unsubscribe route that the rendered text loses", func(req MessagesShowParams) (any, error) {
		client := mail.NewClient()
		in := req.In
		// Asking for the source replaces the rendered-body read rather than
		// adding to it, so either way the message is read once.
		wantSource := req.Body == "html" || req.Body == "links"

		message, err := client.GetMessageDetailsJSON(in.Account, in.Mailbox, req.ID, !wantSource)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to read message %s: %v", req.ID, err)
		}
		if message.ID == "" {
			return nil, errf(CodeMessageMissing, "no message %s in %s/%s",
				req.ID, in.Account, in.Mailbox).
				withHint("re-list the mailbox first: mutations assign new ids")
		}

		data := map[string]any{"message": message}
		if !wantSource {
			return data, nil
		}

		raw, err := client.GetMessageSource(in.Account, in.Mailbox, req.ID)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to read the raw source of %s: %v", req.ID, err)
		}
		parsed, perr := rawmail.Parse(raw)
		if perr != nil {
			return nil, errf(CodeScriptFailed, "could not parse the raw source of %s: %v", req.ID, perr).
				withHint(`the rendered body is still available with "body":"text"`)
		}
		// The text part comes out of the same parse, so it is always included;
		// the html is what "links" leaves out, since it is the bulky half.
		if parsed.Text != "" {
			message.Content = parsed.Text
		}
		if req.Body != "links" {
			data["html"] = parsed.HTML
		}
		data["links"] = parsed.Links
		data["listUnsubscribe"] = parsed.ListUnsubscribe
		data["listUnsubscribeOneClick"] = parsed.ListUnsubscribeOneClick
		return data, nil
	})
}
