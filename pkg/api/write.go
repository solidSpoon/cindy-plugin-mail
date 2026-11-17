package api

import (
	"strings"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
)

// boolOr resolves an optional flag: absent means "leave it alone" at the call
// site, and the caller passes the action's default.
func boolOr(ptr *bool, def bool) bool {
	if ptr == nil {
		return def
	}
	return *ptr
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
		"missing": sum.Missing,
		"failed":  sum.Failed,
	}
	if sum.Missing == 0 && sum.Failed == 0 {
		return data, nil
	}
	return data, errf(CodePartialFailure, "%d of %d messages were not %s",
		sum.Missing+sum.Failed, len(sum.Results), verb)
}

func quarantineError(err error) *Error {
	if err != nil && strings.Contains(err.Error(), "Holding mailbox") {
		return errf(CodeHoldingBoxMissing, "%v", err).
			withHint("create the mailbox in Mail.app, or pass a box that exists")
	}
	return errf(CodeScriptFailed, "quarantine failed: %v", err)
}

// splitIDs reports a mutation's target mailbox, or nil for global resolution.
// A MailboxRef always carries both fields, so "account without mailbox" is not
// expressible and needs no runtime check any more.
func splitRef(ref *MailboxRef) *mailboxRef {
	if ref == nil {
		return nil
	}
	return &mailboxRef{account: ref.Account, mailbox: ref.Mailbox}
}

type mailboxRef struct{ account, mailbox string }

// ------------------------------------------------------------ messages.patch

type MessagesPatchParams struct {
	IDs    []string    `json:"ids" req:"1" desc:"Mail.app message ids; all of them are handled in one call"`
	In     *MailboxRef `json:"in,omitempty" desc:"look the ids up in this mailbox; omit to resolve them globally"`
	Set    StateSet    `json:"set" req:"1" desc:"the flags to set; a field left out is left untouched"`
	DryRun bool        `json:"dryRun,omitempty" desc:"report what would change and change nothing"`
}

func init() {
	Register("messages.patch", "Set read/flagged state on messages in one Mail.app call", func(req MessagesPatchParams) (any, error) {
		if req.Set.Empty() {
			return nil, errf(CodeInvalidRequest, "messages.patch: set must name read and/or flagged")
		}
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		ref := splitRef(req.In)
		client := mail.NewClient()
		out := map[string]any{}

		if req.Set.Read != nil {
			read := *req.Set.Read
			verb := "marked read"
			if !read {
				verb = "marked unread"
			}
			if ref != nil {
				res, err := client.MarkMessages(ref.account, ref.mailbox, req.IDs, read)
				if err != nil {
					return nil, errf(CodeScriptFailed, "mark failed: %v", err)
				}
				data, e := mutationData(res, verb)
				out["read"] = data
				if e != nil && out["error"] == nil {
					out["error"] = e.Error()
				}
				invalidateMailboxCache(ref.account, ref.mailbox)
			} else {
				action := "read"
				if !read {
					action = "unread"
				}
				sum, err := client.MutateMessagesGlobal(req.IDs, action, "")
				if err != nil {
					return nil, errf(CodeScriptFailed, "mark failed: %v", err)
				}
				data, e := globalData(sum, verb)
				out["read"] = data
				if e != nil && out["error"] == nil {
					out["error"] = e.Error()
				}
			}
		}

		if req.Set.Flagged != nil {
			flagged := *req.Set.Flagged
			verb := "flagged"
			if !flagged {
				verb = "unflagged"
			}
			if ref != nil {
				res, err := client.FlagMessages(ref.account, ref.mailbox, req.IDs, flagged)
				if err != nil {
					return nil, errf(CodeScriptFailed, "flag failed: %v", err)
				}
				data, e := mutationData(res, verb)
				out["flagged"] = data
				if e != nil && out["error"] == nil {
					out["error"] = e.Error()
				}
				invalidateMailboxCache(ref.account, ref.mailbox)
			} else {
				action := "flag"
				if !flagged {
					action = "unflag"
				}
				sum, err := client.MutateMessagesGlobal(req.IDs, action, "")
				if err != nil {
					return nil, errf(CodeScriptFailed, "flag failed: %v", err)
				}
				data, e := globalData(sum, verb)
				out["flagged"] = data
				if e != nil && out["error"] == nil {
					out["error"] = e.Error()
				}
			}
		}

		if msg, ok := out["error"].(string); ok {
			delete(out, "error")
			return out, errf(CodePartialFailure, "%s", msg)
		}
		return out, nil
	})
}

// ------------------------------------------------------------- messages.move

type MessagesMoveParams struct {
	IDs    []string    `json:"ids" req:"1"`
	To     string      `json:"to" req:"1" desc:"destination mailbox name inside each message's own account"`
	In     *MailboxRef `json:"in,omitempty" desc:"look the ids up in this mailbox; omit to resolve them globally"`
	DryRun bool        `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.move", "Move messages to another mailbox. The moved messages get new ids in the destination, and the destination is read back to confirm they arrived", func(req MessagesMoveParams) (any, error) {
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		ref := splitRef(req.In)
		client := mail.NewClient()
		before, err := client.ResolveMessages(req.IDs)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to resolve ids: %v", err)
		}
		verb := "moved to " + req.To
		var (
			data    any
			handled []string
		)
		if ref != nil {
			res, err := client.MoveMessages(ref.account, ref.mailbox, req.IDs, req.To)
			if err != nil {
				return nil, errf(CodeScriptFailed, "move failed: %v", err)
			}
			invalidateMailboxCache(ref.account, ref.mailbox)
			invalidateMailboxCache(ref.account, req.To)
			data, _ = mutationData(res, verb)
			handled = res.Succeeded
		} else {
			sum, err := client.MutateMessagesGlobal(req.IDs, "move", req.To)
			if err != nil {
				return nil, errf(CodeScriptFailed, "move failed: %v", err)
			}
			data, _ = globalData(sum, verb)
			handled = succeededIDs(sum)
		}
		report := confirmMoved(client, before, handled, fixedDest(req.To))
		attachReport(data, report)
		return data, relocationError(report, req.To)
	})
}

// ------------------------------------------------------- messages.quarantine

type MessagesQuarantineParams struct {
	IDs []string `json:"ids" req:"1"`
	// def has to mirror mail.DefaultQuarantineMailbox: a struct tag cannot
	// reference a constant.
	Box    string      `json:"box,omitempty" def:"待删除" desc:"holding mailbox name (default 待删除); it must already exist in Mail.app, created there by the user - a mailbox created by scripting stays local-only and this tool never creates one"`
	In     *MailboxRef `json:"in,omitempty" desc:"look the ids up in this mailbox; omit to resolve them globally"`
	DryRun bool        `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.quarantine", "Park messages in a holding mailbox that already exists on the server. This is the safe replacement for deleting: nothing is destroyed, the move is reversible, and the destination is read back to confirm it arrived", func(req MessagesQuarantineParams) (any, error) {
		if req.DryRun {
			return dryRunLocations(req.IDs)
		}
		ref := splitRef(req.In)
		box := req.Box
		if box == "" {
			box = mail.DefaultQuarantineMailbox
		}
		client := mail.NewClient()
		before, err := client.ResolveMessages(req.IDs)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to resolve ids: %v", err)
		}
		var (
			data    any
			handled []string
		)
		if ref != nil {
			res, err := client.QuarantineMessagesIn(ref.account, ref.mailbox, box, req.IDs)
			if err != nil {
				return nil, quarantineError(err)
			}
			invalidateMailboxCache(ref.account, ref.mailbox)
			invalidateMailboxCache(ref.account, box)
			invalidateMailboxListCache()
			data, _ = mutationData(res, "quarantined in "+box)
			handled = res.Succeeded
		} else {
			sum, err := client.QuarantineMessages(box, req.IDs)
			if err != nil {
				return nil, quarantineError(err)
			}
			invalidateMailboxListCache()
			data, _ = globalData(sum, "quarantined in "+box)
			handled = succeededIDs(sum)
		}
		report := confirmMoved(client, before, handled, fixedDest(box))
		attachReport(data, report)
		return data, relocationError(report, box)
	})
}

// ------------------------------------------------------------------ draft

// DraftContent is the body half of a draft.
type DraftContent struct {
	Subject string   `json:"subject" req:"1"`
	Body    string   `json:"body,omitempty" desc:"plain-text body; newlines and quotes need no escaping, it is JSON"`
	Attach  []string `json:"attach,omitempty" desc:"file paths to attach"`
}

type DraftParams struct {
	Account    string       `json:"account,omitempty" desc:"account to draft from; inferred from from, or the only account, when omitted"`
	From       string       `json:"from,omitempty" desc:"From address, e.g. \"名字 <a@b.com>\""`
	Recipients Recipients   `json:"recipients" req:"1" desc:"who the message is addressed to"`
	Content    DraftContent `json:"content" req:"1" desc:"subject, body and attachments"`
	DryRun     bool         `json:"dryRun,omitempty" desc:"report what would be composed without creating the draft"`
}

func init() {
	Register("draft", "Compose a message into Mail.app's Drafts mailbox. Nothing is sent: the user reviews it and presses Send in Mail.app. This tool has no way to send mail", func(req DraftParams) (any, error) {
		if req.DryRun {
			return map[string]any{
				"dryRun":      true,
				"account":     req.Account,
				"to":          req.Recipients.To,
				"cc":          req.Recipients.Cc,
				"bcc":         req.Recipients.Bcc,
				"subject":     req.Content.Subject,
				"bodyBytes":   len(req.Content.Body),
				"attachments": len(req.Content.Attach),
			}, nil
		}
		client := mail.NewClient()
		account := req.Account
		if account == "" && req.From != "" {
			if accounts, err := client.GetAccountsJSON(); err == nil {
				account = matchAccount(accounts, req.From)
			}
		}
		if account == "" {
			return nil, errf(CodeInvalidRequest, "account is required (or give from matching a configured account)")
		}
		result, err := client.SaveDraft(account, req.From, req.Content.Subject, req.Content.Body,
			req.Recipients.To, req.Recipients.Cc, req.Recipients.Bcc, req.Content.Attach)
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
