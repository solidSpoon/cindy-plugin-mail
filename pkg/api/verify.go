package api

import (
	"fmt"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
)

// Relocation results are confirmed by reading the destination back.
//
// This exists because a scripted move can report success and change nothing. A
// mailbox created through Mail.app's scripting interface lives only in the local
// store; the server has never heard of it, so anything "moved" into it is
// re-delivered to its original mailbox under a new id. The move call itself
// cannot tell the difference, which is how 250 messages were once reported as
// changed while every one of them bounced back.
//
// Mail.app assigns new ids on a move, so the messages are tracked by their RFC
// 5322 Message-ID, captured before the move.

// moveReport is what the destination read-back concluded.
type moveReport struct {
	Verified     []string `json:"verified"`
	NotPersisted []string `json:"notPersisted,omitempty"`
	// Unverifiable holds ids whose Message-ID could not be read, so no claim is
	// made about them either way.
	Unverifiable []string `json:"unverifiable,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// destResolver names the mailbox a relocation was aimed at, per account.
type destResolver func(account string) (string, error)

// fixedDest is the destination for move and quarantine: one mailbox name, looked
// up inside each message's own account.
func fixedDest(name string) destResolver {
	return func(string) (string, error) { return name, nil }
}

// archiveDest resolves each account's archive mailbox, which is localized
// ("Archive", "存档", ...) and has no unified Mail.app accessor.
func archiveDest(client *mail.Client) destResolver {
	names := map[string]string{}
	return func(account string) (string, error) {
		if name, ok := names[account]; ok {
			return name, nil
		}
		name, err := client.ArchiveMailboxName(account)
		if err != nil {
			return "", err
		}
		if name == "" {
			return "", fmt.Errorf("no archive mailbox found in %s", account)
		}
		names[account] = name
		return name, nil
	}
}

// confirmMoved reads the destination of every affected account and reports which
// of the handled ids are really there.
func confirmMoved(client *mail.Client, before []mail.MessageLocation, handled []string, dest destResolver) *moveReport {
	report := &moveReport{Verified: []string{}, Unverifiable: []string{}, Warnings: []string{}}
	if len(handled) == 0 {
		return report
	}

	wanted := map[string]bool{}
	for _, id := range handled {
		wanted[id] = true
	}

	// account -> the Message-IDs we expect to find there
	expect := map[string]map[string][]string{}
	for _, loc := range before {
		if !wanted[loc.ID] || !loc.Exists {
			continue
		}
		if loc.MessageID == "" {
			report.Unverifiable = append(report.Unverifiable, loc.ID)
			continue
		}
		if expect[loc.Account] == nil {
			expect[loc.Account] = map[string][]string{}
		}
		expect[loc.Account][loc.MessageID] = append(expect[loc.Account][loc.MessageID], loc.ID)
	}

	for account, byMessageID := range expect {
		mailbox, err := dest(account)
		if err != nil {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("could not resolve the destination in %s, so the move was not confirmed: %v", account, err))
			for _, ids := range byMessageID {
				report.Unverifiable = append(report.Unverifiable, ids...)
			}
			continue
		}
		present, err := client.MailboxMessageIDs(account, mailbox)
		if err != nil {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("could not read %s/%s back, so the move was not confirmed: %v", account, mailbox, err))
			for _, ids := range byMessageID {
				report.Unverifiable = append(report.Unverifiable, ids...)
			}
			continue
		}
		found := make(map[string]bool, len(present))
		for _, messageID := range present {
			found[messageID] = true
		}
		for messageID, ids := range byMessageID {
			if found[messageID] {
				report.Verified = append(report.Verified, ids...)
				continue
			}
			report.NotPersisted = append(report.NotPersisted, ids...)
		}
	}
	return report
}

// relocationError turns a failed confirmation into the error a caller needs to
// act on. Nothing is rolled back: the messages are simply where they were.
//
// It returns the error interface rather than *Error so that the success case is
// an untyped nil: a nil *Error returned as an error is not nil, which silently
// turned confirmed moves into failures.
func relocationError(report *moveReport, destination string) error {
	if len(report.NotPersisted) == 0 {
		return nil
	}
	return errf(CodeMoveNotPersisted,
		"%d of %d messages did not stay in %s; Mail.app reported the move but the destination never received them",
		len(report.NotPersisted), len(report.NotPersisted)+len(report.Verified), destination).
		withHint("a mailbox created by scripting exists only locally and the server sends the message back; create the destination in Mail.app itself, or move to a mailbox that already exists")
}

// attachReport adds the confirmation to a mutation's data map.
func attachReport(data any, report *moveReport) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	m["confirmed"] = len(report.Verified)
	if len(report.Verified) > 0 {
		m["verified"] = report.Verified
	}
	if len(report.NotPersisted) > 0 {
		m["notPersisted"] = report.NotPersisted
	}
	if len(report.Unverifiable) > 0 {
		m["unverifiable"] = report.Unverifiable
	}
	if len(report.Warnings) > 0 {
		m["warnings"] = report.Warnings
	}
}

// succeededIDs lists the ids a global mutation reported as handled.
func succeededIDs(sum *mail.GlobalSummary) []string {
	ids := make([]string, 0, len(sum.Results))
	for _, r := range sum.Results {
		if r.Status == "ok" {
			ids = append(ids, r.ID)
		}
	}
	return ids
}
