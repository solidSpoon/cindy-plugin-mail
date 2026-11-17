package api

import (
	"fmt"
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

// invalidateMailboxCache drops the cached message lists of one mailbox (or of
// every mailbox in an account when mailbox is empty).
func invalidateMailboxCache(account, mailbox string) {
	c, err := cache.New()
	if err != nil {
		return
	}
	prefix := fmt.Sprintf("msgs-%s-", sanitizeCacheKey(account))
	if mailbox != "" {
		prefix = fmt.Sprintf("msgs-%s-%s-", sanitizeCacheKey(account), sanitizeCacheKey(mailbox))
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

func projectMessages(messages []mail.Message, format string) any {
	if format != "compact" {
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

// MessagesListParams reads either one named mailbox or one cross-account view.
//
// The two forms are a single action because they share everything else: the
// filters, pagination, projection and body reading are identical. They cannot be
// a single addressable target, though: a view resolves the right mailbox name
// per account (收件箱 vs INBOX vs All Mail), which no account+mailbox pair can
// express.
type MessagesListParams struct {
	Box         string `json:"box,omitempty" enum:"inbox|unread|flagged|sent|drafts|trash|junk" desc:"read one cross-account view across every account; mutually exclusive with account and mailbox"`
	Account     string `json:"account,omitempty" desc:"account name, e.g. \"Exchange\"; required together with mailbox unless box is given"`
	Mailbox     string `json:"mailbox,omitempty" desc:"mailbox name inside that account, e.g. \"收件箱\"; required together with account unless box is given"`
	Unread      bool   `json:"unread,omitempty" desc:"only unread messages (account+mailbox form only)"`
	Flagged     bool   `json:"flagged,omitempty" desc:"only flagged messages"`
	Since       string `json:"since,omitempty" desc:"only messages received at or after this date: YYYY-MM-DD or YYYY-MM-DD HH:MM:SS"`
	Limit       int    `json:"limit,omitempty" def:"25" desc:"maximum messages to return"`
	Offset      int    `json:"offset,omitempty" desc:"messages to skip (pagination)"`
	WithContent bool   `json:"withContent,omitempty" desc:"include bodies; can block Mail.app while a body downloads, so prefer small mailboxes"`
	WithHeaders bool   `json:"withHeaders,omitempty" desc:"include InReplyTo/References for threading (about 9x slower)"`
	NoCache     bool   `json:"noCache,omitempty" desc:"bypass the short-lived list cache"`
	Format      string `json:"format,omitempty" enum:"full|compact" def:"full" desc:"compact drops fields that are empty for the query"`
}

func init() {
	Register("messages.list", "List messages in one mailbox, or one cross-account view with box", func(req MessagesListParams) (any, error) {
		if req.Limit <= 0 {
			req.Limit = 25
		}
		if req.Box != "" {
			if req.Account != "" || req.Mailbox != "" {
				return nil, errf(CodeInvalidRequest,
					"messages.list: box cannot be combined with account or mailbox")
			}
			if req.Unread || req.Flagged || req.NoCache {
				return nil, errf(CodeInvalidRequest,
					"messages.list: unread, flagged and noCache apply to account+mailbox only; a box view already filters and is never cached")
			}
			messages, err := mail.NewClient().GetUnifiedMessagesJSON(req.Box, req.Limit, req.Offset,
				req.WithContent, req.WithHeaders, req.Since)
			if err != nil {
				return nil, errf(CodeScriptFailed, "failed to read the %s view: %v", req.Box, err)
			}
			return map[string]any{
				"box": req.Box, "count": len(messages), "cached": false,
				"messages": projectMessages(messages, req.Format),
			}, nil
		}
		if req.Account == "" || req.Mailbox == "" {
			return nil, errf(CodeInvalidRequest,
				"messages.list: give box, or both account and mailbox")
		}
		cacheKey := fmt.Sprintf("msgs-%s-%s-%d-%d-%v-%v-%s-%v-%v",
			sanitizeCacheKey(req.Account), sanitizeCacheKey(req.Mailbox),
			req.Limit, req.Offset, req.Unread, req.Flagged,
			sanitizeCacheKey(req.Since), req.WithContent, req.WithHeaders)

		var (
			messages []mail.Message
			err      error
		)
		c, cacheErr := cache.New()
		cached := false
		if !req.NoCache && cacheErr == nil && !req.WithContent {
			c.SetTTL(messagesCacheTTL)
			if found, gerr := c.Get(cacheKey, &messages); gerr == nil && found {
				cached = true
			}
		}
		if !cached {
			messages, err = mail.NewClient().GetMessagesJSON(req.Account, req.Mailbox,
				req.Limit, req.Offset, req.Unread, req.Flagged, req.WithContent, req.WithHeaders, req.Since)
			if err != nil {
				return nil, errf(CodeScriptFailed, "failed to list %s/%s: %v", req.Account, req.Mailbox, err)
			}
			if cacheErr == nil && !req.NoCache {
				c.SetTTL(messagesCacheTTL)
				_ = c.Set(cacheKey, messages)
			}
		}
		return map[string]any{
			"account": req.Account, "mailbox": req.Mailbox,
			"count": len(messages), "cached": cached,
			"messages": projectMessages(messages, req.Format),
		}, nil
	})
}

type MessagesShowParams struct {
	Message MessageInMailbox `json:"message" req:"1"`
	Body    string           `json:"body,omitempty" enum:"text|html|links" def:"text" desc:"text: Mail.app's rendered body, in which hyperlinks are already flattened to their anchor text. html: parse the raw message source and return its html, links and listUnsubscribe. links: the same parse without the html field, when all you need are the addresses."`
}

func init() {
	Register("messages.show", "Show one message in full, including its body and thread headers. body:\"html\" or \"links\" recovers the hyperlinks and the List-Unsubscribe route that the rendered text loses", func(req MessagesShowParams) (any, error) {
		client := mail.NewClient()
		in := req.Message.In
		// Asking for the source replaces the rendered-body read rather than
		// adding to it, so either way the message is read once.
		wantSource := req.Body == "html" || req.Body == "links"

		message, err := client.GetMessageDetailsJSON(in.Account, in.Mailbox, req.Message.ID, !wantSource)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to read message %s: %v", req.Message.ID, err)
		}
		if message.ID == "" {
			return nil, errf(CodeMessageMissing, "no message %s in %s/%s",
				req.Message.ID, in.Account, in.Mailbox).
				withHint("re-list the mailbox first: mutations assign new ids")
		}

		data := map[string]any{"message": message}
		if !wantSource {
			return data, nil
		}

		raw, err := client.GetMessageSource(in.Account, in.Mailbox, req.Message.ID)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to read the raw source of %s: %v", req.Message.ID, err)
		}
		parsed, perr := rawmail.Parse(raw)
		if perr != nil {
			return nil, errf(CodeScriptFailed, "could not parse the raw source of %s: %v", req.Message.ID, perr).
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
