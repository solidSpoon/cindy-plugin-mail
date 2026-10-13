package api

import (
	"strings"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
)

func boolOr(ptr *bool, def bool) bool {
	if ptr == nil {
		return def
	}
	return *ptr
}

// scoped reports whether a mutation should work through a known mailbox.
// Giving one of account/mailbox but not the other is ambiguous.
func scoped(account, mailbox string) (bool, *Error) {
	if account == "" && mailbox == "" {
		return false, nil
	}
	if account == "" || mailbox == "" {
		return false, errf(CodeInvalidRequest,
			"give both account and mailbox, or neither (ids are then resolved globally)")
	}
	return true, nil
}

// dryRunLocations answers "what would this touch?" without touching anything.
func dryRunLocations(ids []string) (any, error) {
	locations, err := mail.NewClient().ResolveMessages(ids)
	if err != nil {
		return nil, errf(CodeScriptFailed, "failed to resolve ids: %v", err)
	}
	wouldAffect := []mail.MessageLocation{}
	missing := []string{}
	for _, loc := range locations {
		if loc.Exists {
			wouldAffect = append(wouldAffect, loc)
		} else {
			missing = append(missing, loc.ID)
		}
	}
	return map[string]any{
		"dryRun":      true,
		"wouldAffect": wouldAffect,
		"missing":     missing,
	}, nil
}

func mutationData(res *mail.MutationResult, verb string) (any, error) {
	total := len(res.Succeeded) + len(res.Missing) + len(res.Failed)
	data := map[string]any{
		"verb":      verb,
		"changed":   len(res.Succeeded),
		"succeeded": res.Succeeded,
		"missing":   res.Missing,
		"failed":    res.Failed,
	}
	if len(res.Missing) == 0 && len(res.Failed) == 0 {
		return data, nil
	}
	return data, errf(CodePartialFailure, "%d of %d messages were not %s",
		len(res.Missing)+len(res.Failed), total, verb)
}

func globalData(sum *mail.GlobalSummary, verb string) (any, error) {
	data := map[string]any{
		"verb":    verb,
		"changed": sum.OK,
		"results": sum.Results,
		"skipped": sum.Skipped,
		"missing": sum.Missing,
		"failed":  sum.Failed,
	}
	if sum.Missing == 0 && sum.Failed == 0 {
		return data, nil
	}
	return data, errf(CodePartialFailure, "%d of %d messages were not %s",
		sum.Missing+sum.Failed, len(sum.Results), verb)
}

// archiveError maps the engine's Gmail refusal onto a stable code so a caller
// knows to reach for messages.quarantine instead of retrying.
func archiveError(err error) *Error {
	if err != nil && strings.Contains(err.Error(), "Gmail accounts cannot be archived") {
		return errf(CodeGmailArchive, "%v", err).
			withHint("Mail.app cannot archive Gmail safely; use messages.quarantine instead")
	}
	return errf(CodeScriptFailed, "archive failed: %v", err)
}

func quarantineError(err error) *Error {
	if err != nil && strings.Contains(err.Error(), "Holding mailbox") {
		return errf(CodeHoldingBoxMissing, "%v", err).
			withHint("create the mailbox in Mail.app, or pass a box that exists")
	}
	return errf(CodeScriptFailed, "quarantine failed: %v", err)
}

// ------------------------------------------------------------------- mark

type MessagesMarkReq struct {
	base
	IDs     []string `json:"ids" req:"1" desc:"Mail.app message ids"`
	Read    *bool    `json:"read,omitempty" def:"true" desc:"true marks read, false marks unread"`
	Account string   `json:"account,omitempty" desc:"with mailbox: look the ids up there instead of resolving them globally"`
	Mailbox string   `json:"mailbox,omitempty"`
	DryRun  bool     `json:"dryRun,omitempty" desc:"report what would change and change nothing"`
}

func init() {
	Register("messages.mark", "Mark messages read or unread in one Mail.app call", func(req MessagesMarkReq) (any, error) {
		read := boolOr(req.Read, true)
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		isScoped, e := scoped(req.Account, req.Mailbox)
		if e != nil {
			return nil, e
		}
		verb := "marked read"
		if !read {
			verb = "marked unread"
		}
		client := mail.NewClient()
		if isScoped {
			res, err := client.MarkMessages(req.Account, req.Mailbox, req.IDs, read)
			if err != nil {
				return nil, errf(CodeScriptFailed, "mark failed: %v", err)
			}
			invalidateMailboxCache(req.Account, req.Mailbox)
			return mutationData(res, verb)
		}
		action := "read"
		if !read {
			action = "unread"
		}
		sum, err := client.MutateMessagesGlobal(req.IDs, action, "", "")
		if err != nil {
			return nil, errf(CodeScriptFailed, "mark failed: %v", err)
		}
		return globalData(sum, verb)
	})
}

// ------------------------------------------------------------------- flag

type MessagesFlagReq struct {
	base
	IDs     []string `json:"ids" req:"1" desc:"Mail.app message ids"`
	Flagged *bool    `json:"flagged,omitempty" def:"true" desc:"true flags, false unflags"`
	Account string   `json:"account,omitempty"`
	Mailbox string   `json:"mailbox,omitempty"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.flag", "Flag or unflag messages in one Mail.app call", func(req MessagesFlagReq) (any, error) {
		flagged := boolOr(req.Flagged, true)
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		isScoped, e := scoped(req.Account, req.Mailbox)
		if e != nil {
			return nil, e
		}
		verb := "flagged"
		if !flagged {
			verb = "unflagged"
		}
		client := mail.NewClient()
		if isScoped {
			res, err := client.FlagMessages(req.Account, req.Mailbox, req.IDs, flagged)
			if err != nil {
				return nil, errf(CodeScriptFailed, "flag failed: %v", err)
			}
			invalidateMailboxCache(req.Account, req.Mailbox)
			return mutationData(res, verb)
		}
		action := "flag"
		if !flagged {
			action = "unflag"
		}
		sum, err := client.MutateMessagesGlobal(req.IDs, action, "", "")
		if err != nil {
			return nil, errf(CodeScriptFailed, "flag failed: %v", err)
		}
		return globalData(sum, verb)
	})
}

// ------------------------------------------------------------------- move

type MessagesMoveReq struct {
	base
	IDs     []string `json:"ids" req:"1"`
	To      string   `json:"to" req:"1" desc:"destination mailbox name inside the message's own account"`
	Account string   `json:"account,omitempty"`
	Mailbox string   `json:"mailbox,omitempty"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.move", "Move messages to another mailbox", func(req MessagesMoveReq) (any, error) {
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		isScoped, e := scoped(req.Account, req.Mailbox)
		if e != nil {
			return nil, e
		}
		client := mail.NewClient()
		verb := "moved to " + req.To
		if isScoped {
			res, err := client.MoveMessages(req.Account, req.Mailbox, req.IDs, req.To)
			if err != nil {
				return nil, errf(CodeScriptFailed, "move failed: %v", err)
			}
			invalidateMailboxCache(req.Account, req.Mailbox)
			invalidateMailboxCache(req.Account, req.To)
			return mutationData(res, verb)
		}
		sum, err := client.MutateMessagesGlobal(req.IDs, "move", req.To, "")
		if err != nil {
			return nil, errf(CodeScriptFailed, "move failed: %v", err)
		}
		return globalData(sum, verb)
	})
}

// ---------------------------------------------------------------- archive

type MessagesArchiveReq struct {
	base
	IDs         []string `json:"ids" req:"1"`
	Account     string   `json:"account,omitempty"`
	Mailbox     string   `json:"mailbox,omitempty"`
	GmailPolicy string   `json:"gmailPolicy,omitempty" enum:"skip|read" def:"skip" desc:"what to do with Gmail messages, which Mail.app cannot archive safely"`
	DryRun      bool     `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.archive", "Move messages to the account's Archive mailbox", func(req MessagesArchiveReq) (any, error) {
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		isScoped, e := scoped(req.Account, req.Mailbox)
		if e != nil {
			return nil, e
		}
		client := mail.NewClient()
		if isScoped {
			res, err := client.ArchiveMessages(req.Account, req.Mailbox, req.IDs)
			if err != nil {
				return nil, archiveError(err)
			}
			invalidateMailboxCache(req.Account, req.Mailbox)
			invalidateMailboxCache(req.Account, "Archive")
			return mutationData(res, "archived")
		}
		sum, err := client.MutateMessagesGlobal(req.IDs, "archive", "", req.GmailPolicy)
		if err != nil {
			return nil, archiveError(err)
		}
		return globalData(sum, "archived")
	})
}

// ------------------------------------------------------------- quarantine

type MessagesQuarantineReq struct {
	base
	IDs     []string `json:"ids" req:"1"`
	Box     string   `json:"box,omitempty" desc:"holding mailbox name, created in each account when missing"`
	Account string   `json:"account,omitempty"`
	Mailbox string   `json:"mailbox,omitempty"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.quarantine", "Park messages in a holding mailbox. This is the safe replacement for deleting: nothing is destroyed and the move is reversible from Mail.app", func(req MessagesQuarantineReq) (any, error) {
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		isScoped, e := scoped(req.Account, req.Mailbox)
		if e != nil {
			return nil, e
		}
		box := req.Box
		if box == "" {
			box = mail.DefaultQuarantineMailbox
		}
		client := mail.NewClient()
		if isScoped {
			res, err := client.QuarantineMessagesIn(req.Account, req.Mailbox, box, req.IDs)
			if err != nil {
				return nil, quarantineError(err)
			}
			invalidateMailboxCache(req.Account, req.Mailbox)
			invalidateMailboxCache(req.Account, box)
			invalidateMailboxListCache()
			return mutationData(res, "quarantined in "+box)
		}
		sum, err := client.QuarantineMessages(box, req.IDs)
		if err != nil {
			return nil, quarantineError(err)
		}
		invalidateMailboxListCache()
		return globalData(sum, "quarantined in "+box)
	})
}

// ------------------------------------------------------------ mark mailbox

type MailboxesMarkReadReq struct {
	base
	Account []string `json:"account,omitempty" desc:"limit to these accounts (repeatable)"`
	Mailbox string   `json:"mailbox,omitempty" desc:"one mailbox; requires exactly one account"`
	Trash   bool     `json:"trash,omitempty" desc:"every account's Trash / Deleted Items"`
	Junk    bool     `json:"junk,omitempty" desc:"every account's Junk / Spam"`
	Archive bool     `json:"archive,omitempty" desc:"every account's Archive mailbox"`
	All     bool     `json:"all,omitempty" desc:"shorthand for trash+junk+archive"`
	Unread  bool     `json:"unread,omitempty" desc:"mark unread instead of read"`
	DryRun  bool     `json:"dryRun,omitempty" desc:"only count what would change"`
}

func init() {
	Register("mailboxes.markRead", "Mark every message in whole mailboxes read (or unread)", func(req MailboxesMarkReadReq) (any, error) {
		if req.All {
			req.Trash, req.Junk, req.Archive = true, true, true
		}
		kinds := []string{}
		if req.Trash {
			kinds = append(kinds, "trash")
		}
		if req.Junk {
			kinds = append(kinds, "junk")
		}
		if req.Archive {
			kinds = append(kinds, "archive")
		}

		client := mail.NewClient()
		read := !req.Unread
		var results []mail.MailboxMarkResult
		switch {
		case req.Mailbox != "" && len(kinds) > 0:
			return nil, errf(CodeInvalidRequest, "mailbox cannot be combined with trash/junk/archive/all")
		case req.Mailbox != "":
			if len(req.Account) != 1 {
				return nil, errf(CodeInvalidRequest, "mailbox requires exactly one account")
			}
			changed, err := client.MarkMailboxRead(req.Account[0], req.Mailbox, read, req.DryRun)
			if err != nil {
				return nil, errf(CodeScriptFailed, "failed to mark %s/%s: %v", req.Account[0], req.Mailbox, err)
			}
			results = []mail.MailboxMarkResult{{Account: req.Account[0], Mailbox: req.Mailbox, Changed: changed}}
		case len(kinds) > 0:
			var err error
			results, err = client.MarkSpecialMailboxesRead(kinds, req.Account, read, req.DryRun)
			if err != nil {
				return nil, errf(CodeScriptFailed, "failed to mark mailboxes: %v", err)
			}
		default:
			return nil, errf(CodeInvalidRequest, "give mailbox with one account, or one of trash/junk/archive/all")
		}

		total := 0
		failures := []string{}
		for _, r := range results {
			total += r.Changed
			if r.Error != "" {
				failures = append(failures, r.Account+"/"+r.Mailbox+": "+r.Error)
			}
			if !req.DryRun {
				invalidateMailboxCache(r.Account, r.Mailbox)
			}
		}
		if !req.DryRun {
			invalidateMailboxListCache()
		}
		verb := "marked read"
		if !read {
			verb = "marked unread"
		}
		data := map[string]any{
			"verb": verb, "changed": total, "mailboxes": results,
		}
		if len(failures) > 0 {
			return data, errf(CodePartialFailure, "%d mailboxes failed: %s",
				len(failures), strings.Join(failures, "; "))
		}
		return data, nil
	})
}

// ------------------------------------------------------------------ draft

type DraftReq struct {
	base
	Account string   `json:"account,omitempty" desc:"account to draft from; inferred from from, or the only account, when omitted"`
	From    string   `json:"from,omitempty" desc:"From address, e.g. \"名字 <a@b.com>\""`
	To      []string `json:"to" req:"1" desc:"To recipients"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	Subject string   `json:"subject" req:"1"`
	Body    string   `json:"body,omitempty" desc:"plain-text body; newlines are fine, nothing needs shell escaping"`
	Attach  []string `json:"attach,omitempty" desc:"file paths to attach"`
	DryRun  bool     `json:"dryRun,omitempty" desc:"report what would be composed without creating the draft"`
}

func init() {
	Register("draft", "Compose a message into Mail.app's Drafts mailbox. Nothing is sent: the user reviews it and presses Send in Mail.app. This tool has no way to send mail", func(req DraftReq) (any, error) {
		if req.DryRun {
			return map[string]any{
				"dryRun":      true,
				"account":     req.Account,
				"to":          req.To,
				"cc":          req.Cc,
				"bcc":         req.Bcc,
				"subject":     req.Subject,
				"bodyBytes":   len(req.Body),
				"attachments": len(req.Attach),
			}, nil
		}
		client := mail.NewClient()
		account := req.Account
		if account == "" && req.From != "" {
			if acc, err := mail.NewClient().GetAccountsJSON(); err == nil {
				account = matchAccount(acc, req.From)
			}
		}
		if account == "" {
			return nil, errf(CodeInvalidRequest, "account is required (or give from matching a configured account)")
		}
		result, err := client.SaveDraft(account, req.From, req.Subject, req.Body,
			req.To, req.Cc, req.Bcc, req.Attach)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to save draft: %v", err)
		}
		data := map[string]any{"draft": result}
		if !result.Verified {
			return data, errf(CodeDraftUnverified,
				"the draft was composed but not found in the Drafts mailbox").
				withHint("look in Mail.app's Drafts mailbox before composing it again")
		}
		return data, nil
	})
}

// matchAccount finds the account whose address (or alias) matches a From value.
func matchAccount(accounts []mail.Account, from string) string {
	want := strings.ToLower(strings.TrimSpace(from))
	if i, j := strings.Index(want, "<"), strings.Index(want, ">"); i >= 0 && j > i {
		want = want[i+1 : j]
	}
	want = strings.TrimSpace(want)
	for _, acc := range accounts {
		if strings.ToLower(acc.EmailAddress) == want {
			return acc.Name
		}
		for _, alias := range acc.EmailAddresses {
			if strings.ToLower(alias) == want {
				return acc.Name
			}
		}
	}
	return ""
}

// ------------------------------------------------------------- sync, open

type SyncReq struct {
	base
	Account string `json:"account,omitempty" desc:"sync one account; omit to sync every account"`
}

func init() {
	Register("sync", "Ask Mail.app to check for new mail now", func(req SyncReq) (any, error) {
		client := mail.NewClient()
		if req.Account != "" {
			if err := client.SyncAccount(req.Account); err != nil {
				return nil, errf(CodeScriptFailed, "failed to sync %s: %v", req.Account, err)
			}
			return map[string]any{"synced": []string{req.Account}}, nil
		}
		if err := client.SyncAllAccounts(); err != nil {
			return nil, errf(CodeScriptFailed, "failed to sync: %v", err)
		}
		return map[string]any{"synced": "all"}, nil
	})
}

type OpenReq struct {
	base
	ID      string `json:"id,omitempty" desc:"message id to reveal"`
	Account string `json:"account,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
}

func init() {
	Register("open", "Activate Mail.app, optionally focusing a mailbox or message. Useful to hand a message to the user", func(req OpenReq) (any, error) {
		if err := mail.NewClient().OpenInMail(req.Account, req.Mailbox, req.ID); err != nil {
			return nil, errf(CodeScriptFailed, "failed to open Mail.app: %v", err)
		}
		return map[string]any{"opened": true, "account": req.Account, "mailbox": req.Mailbox, "id": req.ID}, nil
	})
}
