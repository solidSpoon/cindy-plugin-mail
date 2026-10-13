// Package api is the JSON-facing surface of mail-app-cli. Every capability is
// an action described by a request struct, dispatched by name and answered with
// a uniform envelope; the command line is only a transport for it.
//
// Two invariants hold for every action and are reported by the `schema` action
// so that a caller can discover them without reading this source:
//
//   - No action sends mail. `draft` composes a message into Mail.app's Drafts
//     mailbox and the user presses Send.
//   - No action deletes mail. `messages.quarantine` moves messages into a
//     holding mailbox instead, which stays reversible from Mail.app.
package api

import "fmt"

// Error codes. These are stable: callers branch on them instead of parsing
// messages. See CodeFor for the mapping from internal failures.
const (
	CodeInvalidRequest = "INVALID_REQUEST"
	CodeUnknownAction  = "UNKNOWN_ACTION"
	CodeAccountMissing = "ACCOUNT_NOT_FOUND"
	CodeMailboxMissing = "MAILBOX_NOT_FOUND"
	CodeMessageMissing = "MESSAGE_MISSING"
	// CodeHoldingBoxMissing means the quarantine holding mailbox is absent and
	// could not be created. The action is retryable; nothing was changed.
	CodeHoldingBoxMissing = "HOLDING_BOX_UNAVAILABLE"
	// CodeGmailArchive means the account is Gmail, where Mail.app offers no
	// safe scriptable archive. Use messages.quarantine instead.
	CodeGmailArchive = "GMAIL_ARCHIVE_UNSUPPORTED"
	// CodeDraftUnverified means the draft was composed but not observed in the
	// Drafts mailbox. Look in Mail.app; do not retry blindly.
	CodeDraftUnverified = "DRAFT_UNVERIFIED"
	CodeAttachmentMiss  = "ATTACHMENT_NOT_FOUND"
	// CodePartialFailure means the action ran but not every id was handled. The
	// data field still carries the per-id outcome.
	CodePartialFailure = "PARTIAL_FAILURE"
	CodeScriptFailed   = "MAIL_SCRIPT_ERROR"
	CodeInternal       = "INTERNAL"
)

// Error is a structured failure. Code is meant to be matched on; Message is
// for a human or a model to read; Hint, when present, says what to do next.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *Error) Error() string {
	if e.Hint == "" {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + " (" + e.Hint + ")"
}

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) withHint(format string, args ...any) *Error {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// Result is the outcome of one action.
type Result struct {
	Index  int    `json:"index"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	DryRun bool   `json:"dryRun,omitempty"`
	Data   any    `json:"data,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// Response is the single document every invocation prints.
type Response struct {
	OK      bool     `json:"ok"`
	OKCount int      `json:"okCount"`
	Failed  int      `json:"failed"`
	Results []Result `json:"results"`
	// Error is set only when the request document itself could not be handled,
	// in which case Results is empty.
	Error *Error `json:"error,omitempty"`
}

// asError converts any error into a structured one, mapping the engine's
// message text onto a stable code.
func asError(err error) *Error {
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*Error); ok {
		return apiErr
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// Failure builds an envelope for a request that could not be handled at all.
func Failure(code, format string, args ...any) Response {
	return Response{Results: []Result{}, Error: errf(code, format, args...)}
}
