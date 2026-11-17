package api

import (
	"os"
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

// dryRunScoped answers "what would this touch?" without touching anything.
func dryRunScoped(client *mail.Client, in MailboxRef, ids []string) ([]mail.MessageLocation, []string, error) {
	locations, err := client.ResolveMessages(ids)
	if err != nil {
		return nil, nil, errf(CodeScriptFailed, "failed to resolve ids: %v", err)
	}
	wouldAffect, missing := partitionLocations(locations)
	return wouldAffect, missing, nil
}

func partitionLocations(locations []mail.MessageLocation) ([]mail.MessageLocation, []string) {
	wouldAffect := []mail.MessageLocation{}
	missing := []string{}
	for _, loc := range locations {
		if loc.Exists {
			wouldAffect = append(wouldAffect, loc)
		} else {
			missing = append(missing, loc.ID)
		}
	}
	return wouldAffect, missing
}

func defaultQuarantineBox() string {
	if v := strings.TrimSpace(os.Getenv("MAIL_APP_CLI_QUARANTINE_BOX")); v != "" {
		return v
	}
	return mail.DefaultQuarantineMailbox
}

func checkDestMailbox(client *mail.Client, account, dest string, holding bool) *Error {
	mailboxes, err := client.GetMailboxesJSON(account, false)
	if err != nil {
		return errf(CodeScriptFailed, "failed to verify destination mailbox %q in %s: %v", dest, account, err)
	}
	for _, mb := range mailboxes {
		if mb.Name == dest {
			return nil
		}
	}
	if holding {
		return errf(CodeHoldingBoxMissing, "holding mailbox %q does not exist in %s", dest, account).
			withHint("create the mailbox in Mail.app, or pass a destination that exists via \"to\"")
	}
	return errf(CodeMailboxMissing, "destination mailbox %q does not exist in %s", dest, account).
		withHint("create the mailbox in Mail.app first, or list existing mailboxes with mailboxes.list")
}

func combineMutationError(relocErr, partialErr error) error {
	if relocErr != nil {
		return relocErr
	}
	return partialErr
}

func mutationData(res *mail.MutationResult, verb string) (any, error) {
	total := len(res.Succeeded) + len(res.Missing) + len(res.Failed)
	data := map[string]any{
		"verb":      verb,
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

func quarantineError(err error) *Error {
	if err != nil && strings.Contains(err.Error(), "Holding mailbox") {
		return errf(CodeHoldingBoxMissing, "%v", err).
			withHint("create the mailbox in Mail.app, or pass a destination that exists via \"to\"")
	}
	return errf(CodeScriptFailed, "quarantine failed: %v", err)
}

// ------------------------------------------------------------ messages.patch

type MessagesPatchParams struct {
	IDs    []string   `json:"ids" req:"1" desc:"Mail.app message ids; all of them are handled in one call"`
	In     MailboxRef `json:"in" req:"1" desc:"the mailbox these messages live in"`
	Set    StateSet   `json:"set" req:"1" desc:"the flags to set; a field left out is left untouched"`
	DryRun bool       `json:"dryRun,omitempty" desc:"report what would change and change nothing"`
}

func init() {
	Register("messages.patch", "Set read/flagged state on messages in one mailbox", func(req MessagesPatchParams) (any, error) {
		if req.Set.Empty() {
			return nil, errf(CodeInvalidRequest, "messages.patch: set must name read and/or flagged")
		}
		client := mail.NewClient()
		if req.DryRun {
			wouldAffect, missing, err := dryRunScoped(client, req.In, req.IDs)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"dryRun":      true,
				"in":          req.In,
				"wouldAffect": wouldAffect,
				"missing":     missing,
			}, nil
		}
		out := map[string]any{}

		if req.Set.Read != nil {
			read := *req.Set.Read
			verb := "marked read"
			if !read {
				verb = "marked unread"
			}
			res, err := client.MarkMessages(req.In.Account, req.In.Mailbox, req.IDs, read)
			if err != nil {
				return nil, errf(CodeScriptFailed, "mark failed: %v", err)
			}
			data, e := mutationData(res, verb)
			out["read"] = data
			if e != nil && out["error"] == nil {
				out["error"] = e.Error()
			}
			invalidateMailboxCache(req.In.Account, req.In.Mailbox)
			invalidateMailboxListCache()
		}

		if req.Set.Flagged != nil {
			flagged := *req.Set.Flagged
			verb := "flagged"
			if !flagged {
				verb = "unflagged"
			}
			res, err := client.FlagMessages(req.In.Account, req.In.Mailbox, req.IDs, flagged)
			if err != nil {
				return nil, errf(CodeScriptFailed, "flag failed: %v", err)
			}
			data, e := mutationData(res, verb)
			out["flagged"] = data
			if e != nil && out["error"] == nil {
				out["error"] = e.Error()
			}
			invalidateMailboxCache(req.In.Account, req.In.Mailbox)
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
	IDs    []string   `json:"ids" req:"1"`
	In     MailboxRef `json:"in" req:"1" desc:"the mailbox these messages currently live in"`
	To     string     `json:"to" req:"1" desc:"destination mailbox name inside the same account"`
	DryRun bool       `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.move", "Move messages to another mailbox in the same account. The moved messages get new ids in the destination, and both source and destination are read back to confirm relocation", func(req MessagesMoveParams) (any, error) {
		client := mail.NewClient()
		if req.DryRun {
			if e := checkDestMailbox(client, req.In.Account, req.To, false); e != nil {
				return nil, e
			}
			wouldAffect, missing, err := dryRunScoped(client, req.In, req.IDs)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"dryRun":      true,
				"in":          req.In,
				"to":          req.To,
				"wouldAffect": wouldAffect,
				"missing":     missing,
			}, nil
		}
		before, err := client.ResolveMessages(req.IDs)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to resolve ids: %v", err)
		}
		res, err := client.MoveMessages(req.In.Account, req.In.Mailbox, req.IDs, req.To)
		if err != nil {
			return nil, errf(CodeScriptFailed, "move failed: %v", err)
		}
		invalidateMailboxCache(req.In.Account, req.In.Mailbox)
		invalidateMailboxCache(req.In.Account, req.To)
		invalidateMailboxListCache()
		data, partialErr := mutationData(res, "moved to "+req.To)
		report := confirmMoved(client, before, res.Succeeded, fixedDest(req.To))
		attachReport(data, report)
		return data, combineMutationError(relocationError(report, req.To), partialErr)
	})
}

// ------------------------------------------------------- messages.quarantine

type MessagesQuarantineParams struct {
	IDs      []string   `json:"ids" req:"1"`
	In       MailboxRef `json:"in" req:"1" desc:"the mailbox these messages currently live in"`
	To       string     `json:"to,omitempty" def:"待删除" desc:"holding mailbox name (default 待删除, or MAIL_APP_CLI_QUARANTINE_BOX); it must already exist in Mail.app, created there by the user"`
	MarkRead bool       `json:"markRead,omitempty" desc:"also mark the quarantined messages as read before moving"`
	DryRun   bool       `json:"dryRun,omitempty"`
}

func init() {
	Register("messages.quarantine", "Park messages in a holding mailbox in the same account. This is the safe replacement for deleting: nothing is destroyed, the move is reversible, and both source and destination are read back to confirm", func(req MessagesQuarantineParams) (any, error) {
		to := req.To
		if to == "" {
			to = defaultQuarantineBox()
		}
		client := mail.NewClient()
		if req.DryRun {
			if e := checkDestMailbox(client, req.In.Account, to, true); e != nil {
				return nil, e
			}
			wouldAffect, missing, err := dryRunScoped(client, req.In, req.IDs)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"dryRun":      true,
				"in":          req.In,
				"to":          to,
				"wouldAffect": wouldAffect,
				"missing":     missing,
			}
			if req.MarkRead {
				out["markRead"] = true
			}
			return out, nil
		}
		before, err := client.ResolveMessages(req.IDs)
		if err != nil {
			return nil, errf(CodeScriptFailed, "failed to resolve ids: %v", err)
		}
		if req.MarkRead {
			if _, err := client.MarkMessages(req.In.Account, req.In.Mailbox, req.IDs, true); err != nil {
				return nil, errf(CodeScriptFailed, "mark read before quarantine failed: %v", err)
			}
		}
		res, err := client.QuarantineMessagesIn(req.In.Account, req.In.Mailbox, to, req.IDs)
		if err != nil {
			return nil, quarantineError(err)
		}
		invalidateMailboxCache(req.In.Account, req.In.Mailbox)
		invalidateMailboxCache(req.In.Account, to)
		invalidateMailboxListCache()
		data, partialErr := mutationData(res, "quarantined in "+to)
		if m, ok := data.(map[string]any); ok && req.MarkRead {
			m["markedRead"] = true
		}
		report := confirmMoved(client, before, res.Succeeded, fixedDest(to))
		attachReport(data, report)
		return data, combineMutationError(relocationError(report, to), partialErr)
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
