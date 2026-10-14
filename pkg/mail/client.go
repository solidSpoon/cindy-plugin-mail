package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Client provides an interface to interact with Mail.app via AppleScript
type Client struct{}

// NewClient creates a new Mail.app client
func NewClient() *Client {
	return &Client{}
}

// escapeJSString escapes a string for use in JavaScript single-quoted strings
func escapeJSString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "'", "\\'")   // Escape single quotes
	s = strings.ReplaceAll(s, "\n", "\\n")  // Escape newlines
	s = strings.ReplaceAll(s, "\r", "\\r")  // Escape carriage returns
	s = strings.ReplaceAll(s, "\t", "\\t")  // Escape tabs
	return s
}

// escapeAppleScriptString escapes a string for use in AppleScript double-quoted strings
func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "\"", "\\\"") // Escape double quotes
	return s
}

// runAppleScript executes an AppleScript and returns the output
func (c *Client) runAppleScript(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("applescript error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

// runJXA executes JavaScript for Automation (JXA) and returns the output
func (c *Client) runJXA(script string) (string, error) {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("jxa error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

// Account represents a Mail.app account
type Account struct {
	ID             string
	Name           string
	FullName       string
	EmailAddress   string
	EmailAddresses []string
	AccountType    string
	UserName       string
	Enabled        bool
}

// Mailbox represents a Mail.app mailbox
type Mailbox struct {
	Name        string
	UnreadCount int
	TotalCount  int
	Account     string
}

// Message represents an email message
type Message struct {
	ID            string
	MessageID     string
	Subject       string
	Sender        string
	DateSent      string
	DateReceived  string
	Read          bool
	Flagged       bool
	Deleted       bool
	MessageSize   int
	Content       string
	Mailbox       string
	Account       string
	ToRecipients  []string
	CcRecipients  []string
	BccRecipients []string
	InReplyTo     string   `json:",omitempty"`
	References    []string `json:",omitempty"`
}

// Attachment represents an email attachment
type Attachment struct {
	Name     string
	FileSize int
	MimeType string
}

// jsResolveMailbox defines a JXA helper that resolves a mailbox by walking
// the account's mailbox tree. byName() specifiers are broken for Gmail
// special mailboxes like "All Mail" ("Can't get object"), so scripts must
// match names against enumerated mailbox objects instead.
const jsResolveMailbox = `
function resolveMailbox(acc, name) {
	// Bulk-fetch names at each level (one IPC call) instead of calling
	// name() per mailbox; only descend into sub-mailboxes when no match.
	function walk(container) {
		const names = container.mailboxes.name();
		for (let j = 0; j < names.length; j++) {
			if (names[j] === name) return container.mailboxes.at(j);
		}
		for (let j = 0; j < names.length; j++) {
			try {
				const child = container.mailboxes.at(j);
				if (child.mailboxes.length > 0) {
					const found = walk(child);
					if (found) return found;
				}
			} catch (e) {}
		}
		return null;
	}
	return walk(acc);
}
// resolveMessage returns a message specifier by Mail's global message id,
// or null if no such message exists. byId() is a direct object specifier
// (~10ms) whereas mbox.messages.id() enumerates the whole mailbox (seconds
// on large boxes). Note that byId is not scoped to mbox: message ids are
// unique across the Mail database, so the result is always the message the
// caller identified, even if Gmail reports it under a different label.
function resolveMessage(mbox, id) {
	const n = Number(id);
	if (!isFinite(n)) return null;
	const m = mbox.messages.byId(n);
	try { m.id(); return m; } catch (e) { return null; }
}
// ensureMailbox returns the mailbox called name inside the account,
// creating it at the account's top level when it does not exist yet. Used by
// messages quarantine, which parks messages in a dedicated holding mailbox.
// Several creation spellings are attempted because Mail.app's scripting
// support for mailbox creation differs between release trains.
function ensureMailbox(acc, name) {
	let mb = resolveMailbox(acc, name);
	if (mb) return mb;
	try {
		mail.make({ new: 'mailbox', at: acc.mailboxes, withProperties: { name: name } });
	} catch (e) {
		try {
			acc.mailboxes.push(mail.Mailbox({ name: name }));
		} catch (e2) {
			try {
				mail.make({ new: 'mailbox', at: acc, withProperties: { name: name } });
			} catch (e3) {}
		}
	}
	return resolveMailbox(acc, name);
}
`

// jsParseThreadHeaders extracts In-Reply-To and References from a raw
// "all headers" string. Both headers can fold across lines (RFC 5322
// continuation lines start with whitespace), so each is matched up to the
// next non-indented header line rather than to end-of-line.
const jsParseThreadHeaders = `
function parseThreadHeaders(headers) {
	function field(name) {
		const m = headers.match(new RegExp('^' + name + ':([^\\n]*(?:\\n[ \\t]+[^\\n]*)*)', 'im'));
		return m ? m[1].replace(/\s+/g, ' ').trim() : '';
	}
	const inReplyToRaw = field('In-Reply-To');
	const referencesRaw = field('References');
	const inReplyTo = (inReplyToRaw.match(/<[^>]+>/) || [''])[0];
	const references = referencesRaw.match(/<[^>]+>/g) || [];
	return { inReplyTo: inReplyTo, references: references };
}
`

// DefaultQuarantineMailbox is the holding mailbox used by `messages
// quarantine`. Messages are *moved* here, never deleted, so the operation stays
// reversible from Mail.app.
//
// A dedicated mailbox is used instead of the account's Trash because Mail.app
// permanently empties Trash on its own schedule (see the account properties
// "empty trash frequency" and "empty trash on quit").
const DefaultQuarantineMailbox = "待删除"

// DraftResult describes a draft saved into Mail.app's Drafts mailbox.
type DraftResult struct {
	Status      string   `json:"status"`
	Account     string   `json:"account"`
	Subject     string   `json:"subject"`
	To          []string `json:"to"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Attachments int      `json:"attachments"`
	// ID is the Mail.app message id of the draft once Mail.app persists it.
	ID string `json:"id,omitempty"`
	// Verified is false when the draft could not be found in the Drafts mailbox
	// within the polling window, so the caller knows to look in Mail.app.
	Verified bool `json:"verified"`
	// Syncing is true when Mail.app has stored the draft but has not populated
	// it yet (no recipients visible), which happens while it uploads to the
	// server. There is nothing to do but look at it in Mail.app.
	Syncing bool `json:"syncing,omitempty"`
	// StrayDrafts lists recipient-less copies of this subject that sit next to a
	// complete draft. They should not normally exist; see SaveDraft.
	StrayDrafts []string `json:"strayDrafts,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// SaveDraft composes a message and saves it to Mail.app's Drafts mailbox
// *without sending it*. Sending is deliberately not scriptable: the user
// reviews the draft in Mail.app and presses Send there.
//
// The compose message is never explicitly saved. Both `save` and
// `close saving yes` leave a recipient-less stray copy of the draft behind on
// Exchange accounts - and not immediately: it shows up roughly 20s later, long
// after any reasonable verification window. Compose message persistence is left
// to Mail.app instead, which settles on a single complete draft on both the
// Exchange and Gmail accounts here.
//
// Because that persistence is asynchronous, the result is verified by polling
// the Drafts mailbox and is reported honestly: `verified` only when the draft
// was found, `syncing` when only a not-yet-populated copy is visible yet.
func (c *Client) SaveDraft(accountName, from, subject, body string, to, cc, bcc, attachments []string) (*DraftResult, error) {
	// Escape all recipients
	var toList, ccList, bccList string
	var escapedTo, escapedCc, escapedBcc []string

	for _, addr := range to {
		escapedTo = append(escapedTo, escapeAppleScriptString(addr))
	}
	toList = strings.Join(escapedTo, `", "`)

	for _, addr := range cc {
		escapedCc = append(escapedCc, escapeAppleScriptString(addr))
	}
	ccList = strings.Join(escapedCc, `", "`)

	for _, addr := range bcc {
		escapedBcc = append(escapedBcc, escapeAppleScriptString(addr))
	}
	bccList = strings.Join(escapedBcc, `", "`)

	// Build attachment code
	var attachCodeBuilder strings.Builder
	for _, attPath := range attachments {
		escapedPath := escapeAppleScriptString(attPath)
		fmt.Fprintf(&attachCodeBuilder, `
			try
				make new attachment with properties {file name:"%s"} at after the last paragraph
			on error
				-- Skip files that can't be attached
			end try
`, escapedPath)
	}
	attachCode := attachCodeBuilder.String()

	var setSenderCode string
	if from != "" {
		setSenderCode = fmt.Sprintf(`set sender to "%s"`, escapeAppleScriptString(from))
	} else {
		setSenderCode = `set sender to (item 1 of (email addresses of targetAccount as list))`
	}

	// AppleScript block
	script := fmt.Sprintf(`
	tell application "Mail"
		try
			set targetAccount to account "%s"
			set newMessage to make new outgoing message with properties {subject:"%s", content:"%s", visible:false}

			tell newMessage
				%s

				repeat with addr in {"%s"}
					make new to recipient at end of to recipients with properties {address:addr}
				end repeat

				if "%s" is not "" then
					repeat with addr in {"%s"}
						make new cc recipient at end of cc recipients with properties {address:addr}
					end repeat
				end if

				if "%s" is not "" then
					repeat with addr in {"%s"}
						make new bcc recipient at end of bcc recipients with properties {address:addr}
					end repeat
				end if
%s
			delay 2
			end tell
			return "Success"
		on error errMsg
			return "Error: " & errMsg
		end try
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(subject), escapeAppleScriptString(body),
		setSenderCode,
		toList,
		ccList, ccList,
		bccList, bccList,
		attachCode)

	out, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}
	// The script reports failures in its return value instead of raising, so
	// turn them into real errors rather than a silent success.
	if strings.HasPrefix(out, "Error: ") {
		return nil, fmt.Errorf("%s", strings.TrimPrefix(out, "Error: "))
	}

	if to == nil {
		to = []string{}
	}
	result := &DraftResult{
		Status:      "saved",
		Account:     accountName,
		Subject:     subject,
		To:          to,
		Cc:          cc,
		Bcc:         bcc,
		Attachments: len(attachments),
	}

	// Mail.app persists the compose message on its own schedule; poll for it
	// rather than declaring success on faith.
	copies := &DraftCopies{}
	for attempt := 0; attempt < 8; attempt++ {
		copies, err = c.FindDraftCopies(accountName, subject)
		if err != nil {
			return nil, err
		}
		if len(copies.Complete) > 0 {
			break
		}
		time.Sleep(900 * time.Millisecond)
	}

	switch {
	case len(copies.Complete) > 0:
		result.Verified = true
		result.ID = copies.Complete[0]
	case len(copies.Strays) > 0:
		// Only a not-yet-populated copy is visible: Mail.app is still storing it.
		result.Verified = true
		result.Syncing = true
		result.ID = copies.Strays[0]
		result.Warnings = append(result.Warnings,
			"Mail.app is still storing this draft; the recipient list may not be visible yet - check it in Mail.app before sending")
		return result, nil
	default:
		result.Status = "unverified"
		result.Warnings = append(result.Warnings,
			"the draft was not found in the Drafts mailbox; open Mail.app to check it")
		return result, nil
	}

	if len(copies.Strays) > 0 {
		result.StrayDrafts = copies.Strays
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"Mail.app left %d recipient-less copy/copies of this draft (%s); remove them in Mail.app",
			len(copies.Strays), strings.Join(copies.Strays, ", ")))
	}
	return result, nil
}

// DraftCopies holds the drafts Mail.app has for one subject in one account's
// Drafts mailbox, split into complete drafts (recipients present) and strays
// (recipient-less copies).
type DraftCopies struct {
	Complete []string `json:"complete"`
	Strays   []string `json:"strays"`
}

// jsFindDraftCopies looks up drafts by exact subject in the account's Drafts
// mailbox. Recipient-less copies are reported separately so callers can tell
// Mail.app's stray duplicate from a real draft.
const jsFindDraftCopies = `
const accountName = '%s';
const subject = '%s';
const out = { complete: [], strays: [] };
try {
	const acc = mail.accounts.byName(accountName);
	let drafts = null;
	try {
		const boxes = mail.draftsMailbox().mailboxes();
		for (let i = 0; i < boxes.length; i++) {
			try { if (boxes[i].account().name() === accountName) { drafts = boxes[i]; break; } } catch (e) {}
		}
	} catch (e) {}
	if (drafts) {
		const ids = drafts.messages.id();
		const subjects = drafts.messages.subject();
		for (let i = 0; i < ids.length; i++) {
			if (String(subjects[i]) !== subject) continue;
			const id = Number(ids[i]);
			let recipients = 0;
			try { recipients = drafts.messages.byId(id).toRecipients().length; } catch (e) {}
			if (recipients > 0) out.complete.push(String(id)); else out.strays.push(String(id));
		}
	}
} catch (e) { out.error = String(e); }
JSON.stringify(out);
`

// FindDraftCopies returns the drafts in accountName's Drafts mailbox whose
// subject matches exactly.
func (c *Client) FindDraftCopies(accountName, subject string) (*DraftCopies, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsFindDraftCopies, escapeJSString(accountName), escapeJSString(subject))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	res := &DraftCopies{}
	if output != "" {
		if err := json.Unmarshal([]byte(output), res); err != nil {
			return nil, fmt.Errorf("unexpected output: %s", output)
		}
	}
	sort.Strings(res.Complete)
	sort.Strings(res.Strays)
	return res, nil
}

// GetAccountsJSON retrieves accounts as JSON using JXA
func (c *Client) GetAccountsJSON() ([]Account, error) {
	script := `
const mail = Application('Mail');
const accounts = mail.accounts();
const result = [];

for (let i = 0; i < accounts.length; i++) {
	const acc = accounts[i];
	let addrs = [];
	try { addrs = acc.emailAddresses() || []; } catch (e) {}
	let fn = '';
	try { fn = acc.fullName(); } catch (e) {}
	result.push({
		id: acc.id(),
		name: acc.name(),
		fullName: fn,
		emailAddress: addrs.length > 0 ? addrs[0] : '',
		emailAddresses: addrs,
		userName: acc.userName(),
		enabled: acc.enabled()
	});
}

JSON.stringify(result);
`
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var accounts []Account
	if err := json.Unmarshal([]byte(output), &accounts); err != nil {
		return nil, fmt.Errorf("failed to parse accounts JSON: %w", err)
	}

	return accounts, nil
}

// SyncAccount forces Mail.app to check for new mail (syncs all accounts)
// Note: Mail.app's AppleScript doesn't support per-account sync, so this syncs all accounts
func (c *Client) SyncAccount(accountName string) error {
	// Verify account exists
	script := fmt.Sprintf(`
	tell application "Mail"
		set accountFound to false
		repeat with acc in accounts
			if name of acc is "%s" then
				set accountFound to true
				exit repeat
			end if
		end repeat
		if not accountFound then
			error "Account not found: %s"
		end if
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(accountName))

	_, err := c.runAppleScript(script)
	if err != nil {
		return err
	}

	// Check for new mail (syncs all accounts)
	return c.SyncAllAccounts()
}

// SyncAllAccounts forces Mail.app to check for new mail across all accounts
func (c *Client) SyncAllAccounts() error {
	script := `tell application "Mail" to check for new mail`
	_, err := c.runAppleScript(script)
	return err
}

// OpenInMail opens and activates Mail.app, optionally focusing an account, mailbox, or message
func (c *Client) OpenInMail(accountName, mailboxName, messageID string) error {
	var script string

	if messageID != "" && accountName != "" && mailboxName != "" {
		script = fmt.Sprintf(`
		tell application "Mail"
			activate
			try
				set targetAccount to account "%s"
				set targetMailbox to mailbox "%s" of targetAccount
				set msgs to messages of targetMailbox
				repeat with msg in msgs
					if id of msg as string is "%s" then
						set selected messages of message viewer 1 to {msg}
						open msg
						exit repeat
					end if
				end repeat
			end try
		end tell`,
			escapeAppleScriptString(accountName),
			escapeAppleScriptString(mailboxName),
			escapeAppleScriptString(messageID),
		)
	} else if accountName != "" && mailboxName != "" {
		script = fmt.Sprintf(`
		tell application "Mail"
			activate
			try
				set targetAccount to account "%s"
				set targetMailbox to mailbox "%s" of targetAccount
				set selected mailboxes of message viewer 1 to {targetMailbox}
			end try
		end tell`,
			escapeAppleScriptString(accountName),
			escapeAppleScriptString(mailboxName),
		)
	} else if accountName != "" {
		script = fmt.Sprintf(`
		tell application "Mail"
			activate
			try
				set targetAccount to account "%s"
				set targetMailbox to mailbox "INBOX" of targetAccount
				set selected mailboxes of message viewer 1 to {targetMailbox}
			end try
		end tell`,
			escapeAppleScriptString(accountName),
		)
	} else {
		script = `tell application "Mail" to activate`
	}

	_, err := c.runAppleScript(script)
	return err
}

// GetMailboxesJSON retrieves mailboxes as JSON using JXA. withCounts also
// fills TotalCount, which costs a full enumeration per mailbox (~200ms on
// large ones).
func (c *Client) GetMailboxesJSON(accountName string, withCounts bool) ([]Mailbox, error) {
	// If specific account requested, use single JXA call
	if accountName != "" {
		return c.getMailboxesForSingleAccount(accountName, withCounts)
	}

	// For all accounts, fetch in parallel for better performance
	accounts, err := c.GetAccountsJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}

	if len(accounts) == 0 {
		return []Mailbox{}, nil
	}

	// If only one account total, no need for parallelization
	if len(accounts) == 1 {
		return c.getMailboxesForSingleAccount(accounts[0].Name, withCounts)
	}

	// Use channel to collect results from goroutines
	type result struct {
		mailboxes []Mailbox
		err       error
	}
	results := make(chan result, len(accounts))

	// Launch goroutine for each account
	for _, account := range accounts {
		go func(accName string) {
			mailboxes, err := c.getMailboxesForSingleAccount(accName, withCounts)
			results <- result{mailboxes: mailboxes, err: err}
		}(account.Name)
	}

	// Collect results
	var allMailboxes []Mailbox
	var errors []error
	for i := 0; i < len(accounts); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMailboxes = append(allMailboxes, res.mailboxes...)
		}
	}

	// Return partial results even if some accounts failed
	if len(errors) > 0 && len(allMailboxes) == 0 {
		return nil, fmt.Errorf("failed to get mailboxes from all accounts: %v", errors)
	}

	return allMailboxes, nil
}

// getMailboxesForSingleAccount retrieves mailboxes for a specific account
func (c *Client) getMailboxesForSingleAccount(accountName string, withCounts bool) ([]Mailbox, error) {
	countExpr := "0"
	if withCounts {
		countExpr = "acc.mailboxes.at(j).messages.length"
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const acc = mail.accounts.byName('%s');
	const accName = acc.name();
	// Bulk property reads: one IPC call per property instead of per mailbox.
	const names = acc.mailboxes.name();
	const unread = acc.mailboxes.unreadCount();
	for (let j = 0; j < names.length; j++) {
		try {
			let totalCount = 0;
			try { totalCount = %s; } catch (e) {}
			result.push({
				name: names[j],
				unreadCount: unread[j],
				totalCount: totalCount,
				account: accName
			});
		} catch (e) {
			// Skip mailboxes that can't be queried at all
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(accountName), countExpr)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var mailboxes []Mailbox
	if err := json.Unmarshal([]byte(output), &mailboxes); err != nil {
		return nil, fmt.Errorf("failed to parse mailboxes JSON: %w", err)
	}

	return mailboxes, nil
}

// GetMessagesJSON retrieves messages from a mailbox using JXA
func (c *Client) GetMessagesJSON(accountName, mailboxName string, limit, offset int, unreadOnly, flaggedOnly, withContent, withHeaders bool, since string) ([]Message, error) {
	// Filters operate on an index array using bulk property accessors
	// (M.readStatus() is one IPC call for the whole mailbox).
	unreadFilter := ""
	if unreadOnly {
		unreadFilter = "{ const rs = M.readStatus(); indices = indices.filter(i => !rs[i]); }"
	}

	flaggedFilter := ""
	if flaggedOnly {
		flaggedFilter = "{ const fs = M.flaggedStatus(); indices = indices.filter(i => fs[i]); }"
	}

	sinceFilter := ""
	if since != "" {
		sinceFilter = fmt.Sprintf("{ const sd = new Date('%s'); const allDates = M.dateReceived(); indices = indices.filter(i => { const d = allDates[i]; return d && d >= sd; }); }", escapeJSString(since))
	}

	offsetClause := ""
	if offset > 0 {
		offsetClause = fmt.Sprintf("if (indices.length > %d) indices = indices.slice(%d);", offset, offset)
	}

	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if (indices.length > %d) indices = indices.slice(0, %d);", limit, limit)
	}

	contentField := "content: '',"
	if withContent {
		// content() is never bulk-fetched: it is large and can trigger a
		// blocking body download inside Mail.app.
		contentField = "content: M.byId(cols.id[i]).content() || '',"
	}

	// allHeaders is a large string and ~9x slower to bulk-fetch than the
	// other scalar props, so it is only added to PROPS (and thus fetched at
	// all) when explicitly requested.
	headerProp := ""
	headerFields := "inReplyTo: '', references: [],"
	if withHeaders {
		headerProp = ", 'allHeaders'"
		headerFields = "inReplyTo: parseThreadHeaders(get(i, 'allHeaders') || '').inReplyTo, references: parseThreadHeaders(get(i, 'allHeaders') || '').references,"
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
`+jsResolveMailbox+jsParseThreadHeaders+`
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	const accName = acc.name();
	const mboxName = mbox.name();
	const M = mbox.messages;
	const total = M.length;

	let indices = Array.from({length: total}, (_, i) => i);
	%s
	%s
	%s
	%s
	%s

	// Each per-message property read is one ~10ms Apple event; a bulk read
	// of one property over the whole mailbox costs ~35us per message. So
	// bulk-fetch everything when the mailbox is small relative to the page,
	// otherwise read per-message for just the page.
	const PROPS = ['id', 'subject', 'sender', 'dateReceived', 'dateSent', 'readStatus', 'flaggedStatus', 'deletedStatus', 'messageId'%s];
	// Index specifiers (M.at(i)) re-enumerate the mailbox on every access,
	// so the per-message path resolves by id instead (ids are cheap in bulk).
	const useBulk = total <= Math.max(2000, indices.length * 300);
	const cols = { id: M.id() };
	let get;
	if (useBulk) {
		for (const p of PROPS) if (p !== 'id') cols[p] = M[p]();
		get = (i, p) => cols[p][i];
	} else {
		const refs = {};
		get = (i, p) => {
			if (p === 'id') return cols.id[i];
			if (!refs[i]) refs[i] = M.byId(cols.id[i]);
			return refs[i][p]();
		};
	}

	for (let k = 0; k < indices.length; k++) {
		const i = indices[k];
		try {
			if (get(i, 'deletedStatus')) continue;
			result.push({
				id: String(get(i, 'id')),
				messageId: get(i, 'messageId') || '',
				subject: get(i, 'subject') || '',
				sender: get(i, 'sender') || '',
				dateReceived: (get(i, 'dateReceived') || new Date()).toISOString(),
				dateSent: (get(i, 'dateSent') || new Date()).toISOString(),
				read: get(i, 'readStatus'),
				flagged: get(i, 'flaggedStatus'),
				messageSize: 0,
				%s
				%s
				mailbox: mboxName,
				account: accName
			});
		} catch (e) {
			// Skip messages that cause errors
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(accountName), escapeJSString(mailboxName),
		unreadFilter, flaggedFilter, sinceFilter, offsetClause, limitClause,
		headerProp, contentField, headerFields)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse messages JSON: %w", err)
	}

	return messages, nil
}

// GetMessageDetailsJSON retrieves full details of a specific message.
//
// withContent controls whether the body is fetched. Reading a body is the one
// Apple event that can block Mail.app while a download stalls, and callers that
// are going to parse the raw source instead do not need the rendered text as
// well, so it is opt-out rather than always-on.
func (c *Client) GetMessageDetailsJSON(accountName, mailboxName, messageID string, withContent bool) (*Message, error) {
	contentExpr := "''"
	if withContent {
		contentExpr = "msg.content() || ''"
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
let result = null;
`+jsResolveMailbox+jsParseThreadHeaders+`
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	const target = resolveMessage(mbox, '%s');
	if (target) {
		const msg = target;
		const toRecipients = [];
		const toRecs = msg.toRecipients();
		for (let t = 0; t < toRecs.length; t++) {
			toRecipients.push(toRecs[t].address());
		}

		const ccRecipients = [];
		const ccRecs = msg.ccRecipients();
		for (let c = 0; c < ccRecs.length; c++) {
			ccRecipients.push(ccRecs[c].address());
		}

		const bccRecipients = [];
		const bccRecs = msg.bccRecipients();
		for (let b = 0; b < bccRecs.length; b++) {
			bccRecipients.push(bccRecs[b].address());
		}

		const threadHeaders = parseThreadHeaders(msg.allHeaders() || '');

		result = {
			id: String(msg.id()),
			messageId: msg.messageId() || '',
			subject: msg.subject() || '',
			sender: msg.sender() || '',
			dateReceived: (msg.dateReceived() || new Date()).toISOString(),
			dateSent: (msg.dateSent() || new Date()).toISOString(),
			read: msg.readStatus(),
			flagged: msg.flaggedStatus(),
			messageSize: msg.messageSize(),
			content: %s,
			mailbox: mbox.name(),
			account: acc.name(),
			toRecipients: toRecipients,
			ccRecipients: ccRecipients,
			bccRecipients: bccRecipients,
			inReplyTo: threadHeaders.inReplyTo,
			references: threadHeaders.references
		};
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(accountName), escapeJSString(mailboxName), escapeJSString(messageID), contentExpr)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var message Message
	if err := json.Unmarshal([]byte(output), &message); err != nil {
		return nil, fmt.Errorf("failed to parse message JSON: %w", err)
	}

	return &message, nil
}

// GetMessageSource returns the raw RFC 822 source of a message: what Mail.app
// exposes as `source`, rather than the rendered body that `content` gives.
// The text/html part, its hyperlinks and the List-Unsubscribe headers survive
// only in the source.
func (c *Client) GetMessageSource(accountName, mailboxName, messageID string) (string, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsResolveMailbox+`
let result = '';
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	const target = resolveMessage(mbox, '%s');
	if (target) result = target.source() || '';
} catch (e) {
	result = '';
}
JSON.stringify(result);
`, escapeJSString(accountName), escapeJSString(mailboxName), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return "", err
	}
	var source string
	if err := json.Unmarshal([]byte(output), &source); err != nil {
		return "", fmt.Errorf("unexpected output: %w", err)
	}
	return source, nil
}

// GetAttachmentsJSON retrieves attachments from a message
func (c *Client) GetAttachmentsJSON(accountName, mailboxName, messageID string) ([]Attachment, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
`+jsResolveMailbox+`
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	const target = resolveMessage(mbox, '%s');
	if (target) {
		const attachments = target.mailAttachments();
		for (let a = 0; a < attachments.length; a++) {
			const att = attachments[a];
			let mimeType = 'unknown';
			try {
				mimeType = att.mimeType() || 'unknown';
			} catch (e) {
				// mimeType() sometimes fails in Mail.app
			}
			result.push({
				name: att.name(),
				fileSize: att.fileSize(),
				mimeType: mimeType
			});
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(accountName), escapeJSString(mailboxName), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var attachments []Attachment
	if err := json.Unmarshal([]byte(output), &attachments); err != nil {
		return nil, fmt.Errorf("failed to parse attachments JSON: %w", err)
	}

	return attachments, nil
}

// SaveAttachment saves an attachment to disk
func (c *Client) SaveAttachment(accountName, mailboxName, messageID, attachmentName, savePath string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const app = Application.currentApplication();
app.includeStandardAdditions = true;
`+jsResolveMailbox+`
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	const target = resolveMessage(mbox, '%s');
	if (!target) {
		'Error: Message not found';
	} else {
		const attachments = target.mailAttachments();
		let found = false;
		for (let a = 0; a < attachments.length; a++) {
			if (attachments[a].name() === '%s') {
				const pathObj = Path('%s');
				attachments[a].save({ in: pathObj });
				found = true;
				break;
			}
		}
		if (found) {
			'Success';
		} else {
			'Error: Attachment not found';
		}
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(accountName), escapeJSString(mailboxName), escapeJSString(messageID), escapeJSString(attachmentName), escapeJSString(savePath))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// SearchMessagesJSON searches for messages across mailboxes
// Note: By default only searches INBOX mailboxes for performance reasons
func (c *Client) SearchMessagesJSON(query string, accountName string, mailboxName string, limit int) ([]Message, error) {
	// Set a reasonable default limit if none specified
	if limit == 0 {
		limit = 50
	}

	// If specific mailbox requested, use single JXA call for simplicity
	if mailboxName != "" {
		return c.searchMessagesInSingleMailbox(query, accountName, mailboxName, limit)
	}

	// Get list of mailboxes to search
	mailboxes, err := c.GetMailboxesJSON(accountName, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get mailboxes: %w", err)
	}

	// Filter to only INBOX mailboxes for performance (unless specific account given)
	var mailboxesToSearch []Mailbox
	for _, mbox := range mailboxes {
		if mbox.Name == "INBOX" || mbox.Name == "Inbox" {
			mailboxesToSearch = append(mailboxesToSearch, mbox)
		}
	}

	if len(mailboxesToSearch) == 0 {
		return []Message{}, nil
	}

	// If only one mailbox, no need for parallelization
	if len(mailboxesToSearch) == 1 {
		return c.searchMessagesInSingleMailbox(query, mailboxesToSearch[0].Account, mailboxesToSearch[0].Name, limit)
	}

	// Search mailboxes in parallel
	type result struct {
		messages []Message
		err      error
	}
	results := make(chan result, len(mailboxesToSearch))

	// Launch goroutine for each mailbox
	for _, mbox := range mailboxesToSearch {
		go func(accName, mboxName string) {
			messages, err := c.searchMessagesInSingleMailbox(query, accName, mboxName, limit)
			results <- result{messages: messages, err: err}
		}(mbox.Account, mbox.Name)
	}

	// Collect results
	var allMessages []Message
	var errors []error
	for i := 0; i < len(mailboxesToSearch); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMessages = append(allMessages, res.messages...)
		}
	}

	// Return partial results even if some mailboxes failed
	if len(errors) > 0 && len(allMessages) == 0 {
		return nil, fmt.Errorf("failed to search all mailboxes: %v", errors)
	}

	// Sort by date received (newest first) and apply limit
	sort.Slice(allMessages, func(i, j int) bool {
		return allMessages[i].DateReceived > allMessages[j].DateReceived
	})

	if len(allMessages) > limit {
		allMessages = allMessages[:limit]
	}

	return allMessages, nil
}

// searchMessagesInSingleMailbox searches for messages in a specific mailbox
func (c *Client) searchMessagesInSingleMailbox(query, accountName, mailboxName string, limit int) ([]Message, error) {
	// Use helper for escaping
	escapedQuery := escapeJSString(query)
	escapedAccount := escapeJSString(accountName)
	escapedMailbox := escapeJSString(mailboxName)

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const searchTerm = '%s'.toLowerCase();
const maxResults = %d;
`+jsResolveMailbox+`
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	const accName = acc.name();
	const mboxName = mbox.name();
	const messages = mbox.messages();
	// Limit how many messages to check per mailbox for performance
	// Messages are typically sorted newest first, so this checks recent messages
	const maxToCheck = Math.min(messages.length, 500);

	for (let k = 0; k < maxToCheck && result.length < maxResults; k++) {
		const msg = messages[k];
		try {
			const subject = (msg.subject() || '').toLowerCase();
			const sender = (msg.sender() || '').toLowerCase();

			// Only search subject and sender
			if (subject.includes(searchTerm) || sender.includes(searchTerm)) {
				result.push({
					id: String(msg.id()),
					messageId: msg.messageId() || '',
					subject: msg.subject() || '',
					sender: msg.sender() || '',
					dateReceived: (msg.dateReceived() || new Date()).toISOString(),
					dateSent: (msg.dateSent() || new Date()).toISOString(),
					read: msg.readStatus(),
					flagged: msg.flaggedStatus(),
					messageSize: msg.messageSize(),
					mailbox: mboxName,
					account: accName
				});
			}
		} catch (e) {
			// Skip messages that cause errors
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapedQuery, limit, escapedAccount, escapedMailbox)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse search results JSON: %w", err)
	}

	return messages, nil
}

// GetMessagesFromMultipleMailboxes loads messages from multiple mailboxes concurrently
func (c *Client) GetMessagesFromMultipleMailboxes(requests []struct {
	AccountName string
	MailboxName string
	Limit       int
	Offset      int
	UnreadOnly  bool
	FlaggedOnly bool
	WithContent bool
	WithHeaders bool
	Since       string
}) ([]Message, error) {
	if len(requests) == 0 {
		return []Message{}, nil
	}

	// If only one request, no need for parallelization
	if len(requests) == 1 {
		req := requests[0]
		return c.GetMessagesJSON(req.AccountName, req.MailboxName, req.Limit, req.Offset, req.UnreadOnly, req.FlaggedOnly, req.WithContent, req.WithHeaders, req.Since)
	}

	// Load messages from multiple mailboxes in parallel
	type result struct {
		messages []Message
		err      error
	}
	results := make(chan result, len(requests))

	// Launch goroutine for each mailbox
	for _, req := range requests {
		go func(r struct {
			AccountName string
			MailboxName string
			Limit       int
			Offset      int
			UnreadOnly  bool
			FlaggedOnly bool
			WithContent bool
			WithHeaders bool
			Since       string
		}) {
			messages, err := c.GetMessagesJSON(r.AccountName, r.MailboxName, r.Limit, r.Offset, r.UnreadOnly, r.FlaggedOnly, r.WithContent, r.WithHeaders, r.Since)
			results <- result{messages: messages, err: err}
		}(req)
	}

	// Collect results
	var allMessages []Message
	var errors []error
	for i := 0; i < len(requests); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMessages = append(allMessages, res.messages...)
		}
	}

	// Return partial results even if some mailboxes failed
	if len(errors) > 0 && len(allMessages) == 0 {
		return nil, fmt.Errorf("failed to get messages from all mailboxes: %v", errors)
	}

	return allMessages, nil
}

// BulkMoveMessages moves multiple messages concurrently
// GetUnifiedMessagesJSON retrieves messages from Mail.app's special unified
// mailboxes (inboxes, sentMailboxes, draftMailboxes, trashMailboxes,
// junkMailboxes) across all accounts in a single JXA call.
//
// mailboxType must be one of: "inbox", "unread", "sent", "drafts",
// "trash", "junk", "flagged".
//
// "unread" and "flagged" are treated as inbox views with the appropriate
// filter applied.
// GetUnifiedMessagesJSON retrieves messages from unified views across all accounts.
//
// mailboxType must be one of: "inbox", "unread", "flagged", "sent", "drafts",
// "trash", "junk".
//
// inbox/unread/flagged use the accounts-based path (GetMessagesFromMultipleMailboxes
// → GetMessagesJSON per account INBOX) because mailbox objects from
// mail.inboxes() don't support the same bulk property operations as those
// resolved from the account's own mailbox tree, causing unreliable filtering.
//
// sent/drafts/trash/junk use Mail.app's JXA special-mailbox accessors
// (mail.sentMailboxes() etc.) which don't require per-message filtering.
func (c *Client) GetUnifiedMessagesJSON(mailboxType string, limit, offset int, withContent, withHeaders bool, since string) ([]Message, error) {
	switch mailboxType {
	case "inbox", "unread", "flagged":
		return c.getInboxBasedUnified(mailboxType, limit, offset, withContent, withHeaders, since)
	case "sent", "drafts", "trash", "junk":
		return c.getSpecialMailboxUnified(mailboxType, limit, offset, withContent, withHeaders, since)
	default:
		return nil, fmt.Errorf("unknown unified mailbox type: %s", mailboxType)
	}
}

// MessageLocation reports where a Mail.app message id lives. ResolveMessages is
// the read-only half of a mutation, used to answer "what would this touch?"
// before anything is changed.
type MessageLocation struct {
	ID      string `json:"id"`
	Exists  bool   `json:"exists"`
	Account string `json:"account,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
}

// ResolveMessages reports, for each id, whether it exists and which mailbox it
// lives in. It never modifies anything. byId is available through any
// container and mail ids are unique database-wide, so the unified inbox is used
// as the lookup container (see MutateMessagesGlobal).
func (c *Client) ResolveMessages(messageIDs []string) ([]MessageLocation, error) {
	if len(messageIDs) == 0 {
		return []MessageLocation{}, nil
	}
	idsJSON, _ := json.Marshal(messageIDs)
	script := fmt.Sprintf(`
const mail = Application('Mail');
const wanted = %s;
const out = [];
const container = mail.inbox().messages;
for (const id of wanted) {
	const r = { id: String(id), exists: false };
	try {
		const mbox = container.byId(Number(id)).mailbox();
		r.account = mbox.account().name();
		r.mailbox = mbox.name();
		r.exists = true;
	} catch (e) {}
	out.push(r);
}
JSON.stringify(out);
`, string(idsJSON))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var out []MessageLocation
	if err := json.Unmarshal([]byte(output), &out); err != nil {
		return nil, fmt.Errorf("unexpected output: %s", output)
	}
	return out, nil
}

// getInboxBasedUnified fetches messages from each account's INBOX using the
// proven GetMessagesJSON path, then merges, sorts, and slices globally.
func (c *Client) getInboxBasedUnified(mailboxType string, limit, offset int, withContent, withHeaders bool, since string) ([]Message, error) {
	// Resolve each account's real inbox name ("INBOX" vs "Inbox") via Mail's
	// unified inbox rather than assuming a name.
	refs, err := c.GetSpecialMailboxRefs("inbox")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve inboxes: %w", err)
	}

	// Over-fetch per account so the global sort+slice is accurate.
	perLimit := limit + offset
	if perLimit < 50 {
		perLimit = 50
	}

	type req = struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		WithHeaders bool
		Since       string
	}

	var requests []req
	for _, r := range refs {
		requests = append(requests, req{
			AccountName: r.Account,
			MailboxName: r.Mailbox,
			Limit:       perLimit,
			Offset:      0,
			UnreadOnly:  mailboxType == "unread",
			FlaggedOnly: mailboxType == "flagged",
			WithContent: withContent,
			WithHeaders: withHeaders,
			Since:       since,
		})
	}

	if len(requests) == 0 {
		return []Message{}, nil
	}

	messages, err := c.GetMessagesFromMultipleMailboxes(requests)
	if err != nil {
		return nil, err
	}

	return sortAndSlice(messages, offset, limit), nil
}

// SpecialMailboxRef names one account's mailbox within a Mail.app unified
// mailbox (inbox, sentMailbox, draftsMailbox, trashMailbox, junkMailbox).
type SpecialMailboxRef struct {
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
}

// GetSpecialMailboxRefs resolves the per-account mailboxes behind one of
// Mail.app's unified mailboxes in a single JXA call, so provider naming
// ("Inbox" vs "INBOX", "Trash" vs "Deleted Items") is handled by Mail.
// kind is one of inbox, sent, drafts, trash, junk.
func (c *Client) GetSpecialMailboxRefs(kind string) ([]SpecialMailboxRef, error) {
	accessor, ok := map[string]string{
		"inbox":  "inbox",
		"sent":   "sentMailbox",
		"drafts": "draftsMailbox",
		"trash":  "trashMailbox",
		"junk":   "junkMailbox",
	}[kind]
	if !ok {
		return nil, fmt.Errorf("unknown special mailbox kind: %s", kind)
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const subs = mail.%s().mailboxes();
for (let i = 0; i < subs.length; i++) {
	try { result.push({ account: subs[i].account().name(), mailbox: subs[i].name() }); } catch (e) {}
}
JSON.stringify(result);
`, accessor)
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var refs []SpecialMailboxRef
	if err := json.Unmarshal([]byte(output), &refs); err != nil {
		return nil, fmt.Errorf("failed to parse mailbox refs: %w", err)
	}
	return refs, nil
}

// getSpecialMailboxUnified lists a unified special mailbox by resolving the
// per-account boxes and fetching each concurrently through GetMessagesJSON
// (the bulk/byId fast path), then merging, sorting and slicing globally.
func (c *Client) getSpecialMailboxUnified(mailboxType string, limit, offset int, withContent, withHeaders bool, since string) ([]Message, error) {
	refs, err := c.GetSpecialMailboxRefs(mailboxType)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []Message{}, nil
	}

	perLimit := limit + offset
	if perLimit < 50 {
		perLimit = 50
	}

	var requests []struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		WithHeaders bool
		Since       string
	}
	for _, r := range refs {
		requests = append(requests, struct {
			AccountName string
			MailboxName string
			Limit       int
			Offset      int
			UnreadOnly  bool
			FlaggedOnly bool
			WithContent bool
			WithHeaders bool
			Since       string
		}{AccountName: r.Account, MailboxName: r.Mailbox, Limit: perLimit, WithContent: withContent, WithHeaders: withHeaders, Since: since})
	}

	messages, err := c.GetMessagesFromMultipleMailboxes(requests)
	if err != nil {
		return nil, err
	}
	return sortAndSlice(messages, offset, limit), nil
}

// sortAndSlice sorts messages by date descending then applies offset and limit.
func sortAndSlice(messages []Message, offset, limit int) []Message {
	if messages == nil {
		messages = []Message{}
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].DateReceived > messages[j].DateReceived
	})
	if offset > 0 {
		if offset >= len(messages) {
			return []Message{}
		}
		messages = messages[offset:]
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[:limit]
	}
	return messages
}

// MutationResult reports the per-message outcome of a batch mutation.
type MutationResult struct {
	Succeeded []string          `json:"succeeded"`
	Missing   []string          `json:"missing"`
	Failed    map[string]string `json:"failed,omitempty"`
}

// Err returns a non-nil error if any message was missing or failed.
func (r *MutationResult) Err() error {
	if len(r.Missing) == 0 && len(r.Failed) == 0 {
		return nil
	}
	var parts []string
	if len(r.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("not found: %s", strings.Join(r.Missing, ", ")))
	}
	for id, msg := range r.Failed {
		parts = append(parts, fmt.Sprintf("%s: %s", id, msg))
	}
	return fmt.Errorf("%d of %d messages failed (%s)",
		len(r.Missing)+len(r.Failed), len(r.Succeeded)+len(r.Missing)+len(r.Failed), strings.Join(parts, "; "))
}

// mutateMessages resolves a mailbox once, looks up every requested message ID
// in a single enumeration, and runs jsAction on each match. jsAction is a JXA
// snippet with `msg` (the message specifier), `acc`, `mbox` and `mail` in
// scope. Each message is resolved with byId (no mailbox enumeration) and all
// IDs share one osascript process, so cost is ~10ms per message plus one
// process launch.
func (c *Client) mutateMessages(accountName, mailboxName string, messageIDs []string, jsPrelude, jsAction string) (*MutationResult, error) {
	res := &MutationResult{Failed: map[string]string{}}
	if len(messageIDs) == 0 {
		return res, nil
	}
	idsJSON, _ := json.Marshal(messageIDs)

	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsResolveMailbox+`
const result = { succeeded: [], missing: [], failed: {} };
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	%s
	const wanted = %s;
	for (const id of wanted) {
		const msg = resolveMessage(mbox, id);
		if (!msg) { result.missing.push(id); continue; }
		const t = { id: id };
		try {
			%s
			result.succeeded.push(t.id);
		} catch (e) {
			result.failed[t.id] = String(e);
		}
	}
	JSON.stringify(result);
} catch (e) {
	JSON.stringify({ error: String(e) });
}
`, escapeJSString(accountName), escapeJSString(mailboxName), jsPrelude, string(idsJSON), jsAction)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Error     string            `json:"error"`
		Succeeded []string          `json:"succeeded"`
		Missing   []string          `json:"missing"`
		Failed    map[string]string `json:"failed"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("unexpected output: %s", output)
	}
	if raw.Error != "" {
		return nil, fmt.Errorf("%s", raw.Error)
	}
	res.Succeeded = raw.Succeeded
	res.Missing = raw.Missing
	if raw.Failed != nil {
		res.Failed = raw.Failed
	}
	return res, nil
}

// MarkMessages marks several messages read/unread in one Mail.app round trip.
func (c *Client) MarkMessages(accountName, mailboxName string, messageIDs []string, read bool) (*MutationResult, error) {
	return c.mutateMessages(accountName, mailboxName, messageIDs, "", fmt.Sprintf("msg.readStatus = %t;", read))
}

// FlagMessages flags/unflags several messages in one Mail.app round trip.
func (c *Client) FlagMessages(accountName, mailboxName string, messageIDs []string, flagged bool) (*MutationResult, error) {
	return c.mutateMessages(accountName, mailboxName, messageIDs, "", fmt.Sprintf("msg.flaggedStatus = %t;", flagged))
}

// MoveMessages moves several messages to targetMailbox in one round trip.
func (c *Client) MoveMessages(accountName, sourceMailbox string, messageIDs []string, targetMailbox string) (*MutationResult, error) {
	prelude := fmt.Sprintf(`const destMbox = resolveMailbox(acc, '%s');
	if (!destMbox) throw 'Destination mailbox not found';`, escapeJSString(targetMailbox))
	return c.mutateMessages(accountName, sourceMailbox, messageIDs, prelude, "mail.move(msg, { to: destMbox });")
}

// ArchiveMessages moves messages to the provider's archive mailbox.
//
// Gmail accounts (detected by the presence of an "All Mail" mailbox) are
// refused: Mail.app offers no safe, scriptable archive for Gmail. Every
// mechanism was tested and fails:
//   - Scripted moves out of INBOX (mailbox property or the move command)
//     behave as label copies on Gmail's IMAP bridge — the message reappears
//     in the inbox on the next sync.
//   - Moving through Trash (move to Trash, then move back out to All Mail)
//     does archive, but races Gmail's "expunged from Trash = permanently
//     deleted" rule and was observed to permanently delete messages.
//   - message.deletedStatus is read-only.
//   - Driving Mail's own Archive command works reliably but requires UI
//     automation that activates Mail and steals focus.
//
// Archiving Gmail from the CLI therefore needs the Gmail API, not Mail.app.
//
// Other providers (Exchange, Fastmail, ...) do real moves, so a plain move
// to their "Archive" mailbox works.
//
// All messages are processed in one Mail.app round trip.
func (c *Client) ArchiveMessages(accountName, mailboxName string, messageIDs []string) (*MutationResult, error) {
	prelude := `if (resolveMailbox(acc, 'All Mail')) throw 'Gmail accounts cannot be archived safely via Mail.app scripting. ' +
		'Archive it in Mail.app or Gmail directly, or use "messages quarantine" to park it in a holding mailbox.';
	const destMbox = resolveMailbox(acc, 'Archive');
	if (!destMbox) throw 'Archive mailbox not found';`
	return c.mutateMessages(accountName, mailboxName, messageIDs, prelude, "mail.move(msg, { to: destMbox });")
}

// QuarantineMessages moves several messages into a holding mailbox instead of
// deleting them, creating that mailbox inside each message's own account when
// it does not exist yet. Nothing is destroyed and the move is reversible from
// Mail.app, so this is the safe replacement for `messages delete`.
//
// Creation and the move run in separate osascript processes: a mailbox created
// inside the same script that moves messages into it hands back an unusable
// specifier ("Can't get object"), because Mail.app has not refreshed its
// mailbox list yet.
func (c *Client) QuarantineMessages(mailboxName string, messageIDs []string) (*GlobalSummary, error) {
	if mailboxName == "" {
		mailboxName = DefaultQuarantineMailbox
	}
	accounts, err := c.HoldingMailboxAccounts(messageIDs)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if _, err := c.EnsureHoldingMailbox(account, mailboxName); err != nil {
			return nil, err
		}
	}
	return c.MutateMessagesGlobal(messageIDs, "quarantine", mailboxName, "")
}

// QuarantineMessagesIn is the scoped form of QuarantineMessages: the messages
// are looked up in one known mailbox instead of being resolved globally.
func (c *Client) QuarantineMessagesIn(accountName, mailboxName, boxName string, messageIDs []string) (*MutationResult, error) {
	if boxName == "" {
		boxName = DefaultQuarantineMailbox
	}
	if _, err := c.EnsureHoldingMailbox(accountName, boxName); err != nil {
		return nil, err
	}
	box := escapeJSString(boxName)
	prelude := fmt.Sprintf(`const destMbox = resolveMailbox(acc, '%s');
	if (!destMbox) throw 'Holding mailbox "%s" is missing in ' + acc.name();`, box, box)
	return c.mutateMessages(accountName, mailboxName, messageIDs, prelude, "mail.move(msg, { to: destMbox });")
}

// HoldingMailboxAccounts returns the distinct account names that own the given
// Mail.app message ids, so a holding mailbox can be created only where it is
// actually needed. Ids that cannot be resolved are ignored.
func (c *Client) HoldingMailboxAccounts(messageIDs []string) ([]string, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	idsJSON, _ := json.Marshal(messageIDs)
	script := fmt.Sprintf(`
const mail = Application('Mail');
const wanted = %s;
const seen = {};
const accounts = [];
const container = mail.inbox().messages;
for (const id of wanted) {
	try {
		const name = container.byId(Number(id)).mailbox().account().name();
		if (!seen[name]) { seen[name] = true; accounts.push(name); }
	} catch (e) {}
}
JSON.stringify(accounts);
`, string(idsJSON))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var accounts []string
	if err := json.Unmarshal([]byte(output), &accounts); err != nil {
		return nil, fmt.Errorf("unexpected output: %s", output)
	}
	return accounts, nil
}

// EnsureHoldingMailbox creates boxName inside accountName when it does not
// exist yet and reports whether it created it. It runs in its own osascript
// process on purpose: see QuarantineMessages.
func (c *Client) EnsureHoldingMailbox(accountName, boxName string) (bool, error) {
	if boxName == "" {
		boxName = DefaultQuarantineMailbox
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsResolveMailbox+`
const acc = mail.accounts.byName('%s');
const name = '%s';
if (resolveMailbox(acc, name)) {
	JSON.stringify({ created: false });
} else {
	ensureMailbox(acc, name);
	JSON.stringify({ created: true });
}
`, escapeJSString(accountName), escapeJSString(boxName))

	output, err := c.runJXA(script)
	if err != nil {
		return false, err
	}
	var res struct {
		Created bool `json:"created"`
	}
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		return false, fmt.Errorf("unexpected output: %s", output)
	}
	return res.Created, nil
}

// MailboxMarkResult reports one mailbox touched by a mark-read operation.
type MailboxMarkResult struct {
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
	Changed int    `json:"changed"`
	Error   string `json:"error,omitempty"`
}

// jsMarkMailbox marks every message in `mb` whose readStatus != read and
// returns the count. Bulk property assignment on a whose() specifier is not
// supported by Mail.app, so this loops over the filtered result.
const jsMarkMailbox = `
function markMailbox(mb, read, dryRun) {
	// unreadCount is a cheap property; skip the full whose() scan when
	// there is nothing to do (only valid when marking read).
	if (read) { try { if (mb.unreadCount() === 0) return 0; } catch (e) {} }
	const pending = mb.messages.whose({ readStatus: !read })();
	if (!dryRun) {
		for (let i = 0; i < pending.length; i++) pending[i].readStatus = read;
	}
	return pending.length;
}
`

// MarkMailboxRead marks all messages in one mailbox read (or unread) and
// returns the number of messages changed. With dryRun it only counts.
func (c *Client) MarkMailboxRead(accountName, mailboxName string, read, dryRun bool) (int, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsResolveMailbox+jsMarkMailbox+`
try {
	const acc = mail.accounts.byName('%s');
	const mbox = resolveMailbox(acc, '%s');
	if (!mbox) throw 'Mailbox not found';
	JSON.stringify({ changed: markMailbox(mbox, %t, %t) });
} catch (e) {
	JSON.stringify({ error: String(e) });
}
`, escapeJSString(accountName), escapeJSString(mailboxName), read, dryRun)

	output, err := c.runJXA(script)
	if err != nil {
		return 0, err
	}
	var raw struct {
		Error   string `json:"error"`
		Changed int    `json:"changed"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return 0, fmt.Errorf("unexpected output: %s", output)
	}
	if raw.Error != "" {
		return 0, fmt.Errorf("%s", raw.Error)
	}
	return raw.Changed, nil
}

// MarkSpecialMailboxesRead marks every per-account mailbox of the given kinds
// read (or unread) in a single JXA call. kinds may contain "trash", "junk"
// and "archive". Trash and junk resolve through Mail.app's unified
// trashMailbox/junkMailbox properties, so provider naming (Trash vs
// "Deleted Items", Spam vs "Junk Email") is handled by Mail itself. Archive
// resolves a mailbox literally named "Archive" in each account's tree;
// Gmail's "All Mail" is deliberately not treated as archive because it also
// contains every inbox message. accounts, if non-empty, restricts to those
// account names.
func (c *Client) MarkSpecialMailboxesRead(kinds []string, accounts []string, read, dryRun bool) ([]MailboxMarkResult, error) {
	kindsJSON, _ := json.Marshal(kinds)
	if accounts == nil {
		accounts = []string{}
	}
	accountsJSON, _ := json.Marshal(accounts)

	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsResolveMailbox+jsMarkMailbox+`
const kinds = %s;
const onlyAccounts = %s;
const read = %t;
const dryRun = %t;
const results = [];
function wanted(accName) {
	return onlyAccounts.length === 0 || onlyAccounts.indexOf(accName) >= 0;
}
function handle(accName, mb) {
	const r = { account: accName, mailbox: '', changed: 0 };
	try {
		r.mailbox = mb.name();
		r.changed = markMailbox(mb, read, dryRun);
	} catch (e) {
		r.error = String(e);
	}
	results.push(r);
}
for (const kind of kinds) {
	if (kind === 'trash' || kind === 'junk') {
		const subs = mail[kind + 'Mailbox']().mailboxes();
		for (let i = 0; i < subs.length; i++) {
			let accName = '';
			try { accName = subs[i].account().name(); } catch (e) { continue; }
			if (wanted(accName)) handle(accName, subs[i]);
		}
	} else if (kind === 'archive') {
		const accs = mail.accounts();
		for (let i = 0; i < accs.length; i++) {
			const accName = accs[i].name();
			if (!wanted(accName)) continue;
			let mb = null;
			try { mb = resolveMailbox(accs[i], 'Archive'); } catch (e) {}
			if (mb) handle(accName, mb);
		}
	} else {
		results.push({ account: '', mailbox: kind, changed: 0, error: 'unknown mailbox kind' });
	}
}
JSON.stringify(results);
`, string(kindsJSON), string(accountsJSON), read, dryRun)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var results []MailboxMarkResult
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		return nil, fmt.Errorf("unexpected output: %s", output)
	}
	return results, nil
}

// GlobalResult is the per-message outcome of a global (no account/mailbox)
// mutation. Status is one of ok, missing, failed, skipped.
type GlobalResult struct {
	ID      string `json:"id"`
	Account string `json:"account,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
	Status  string `json:"status"`
	Gmail   bool   `json:"gmail,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GlobalSummary aggregates GlobalResults.
type GlobalSummary struct {
	Results []GlobalResult `json:"results"`
	OK      int            `json:"ok"`
	Missing int            `json:"missing"`
	Failed  int            `json:"failed"`
	Skipped int            `json:"skipped"`
}

// Err returns an error if any message was missing or failed (skips are not
// errors: they are reported deliberately).
func (s *GlobalSummary) Err() error {
	if s.Missing == 0 && s.Failed == 0 {
		return nil
	}
	var parts []string
	for _, r := range s.Results {
		switch r.Status {
		case "missing":
			parts = append(parts, r.ID+": not found")
		case "failed":
			parts = append(parts, r.ID+": "+r.Error)
		}
	}
	return fmt.Errorf("%d of %d messages failed (%s)", s.Missing+s.Failed, len(s.Results), strings.Join(parts, "; "))
}

// MutateMessagesGlobal applies one action to messages identified only by
// their Mail.app IDs. IDs are unique across the whole Mail database, so no
// account or mailbox is needed: each message is resolved with byId and its
// own mailbox().account() supplies the context. Messages from any mix of
// accounts can be handled in one round trip.
//
// action: "read", "unread", "flag", "unflag", "quarantine" (target is the
// holding mailbox name, created inside each message's own account when
// missing), "move" (target is the destination mailbox name within each
// message's own account) or "archive". For archive, gmailPolicy decides what
// happens to messages in Gmail accounts (which cannot be archived safely via
// scripting, see ArchiveMessages): "skip" (default) or "read" (just mark read).
//
// There is deliberately no "delete" action.
func (c *Client) MutateMessagesGlobal(messageIDs []string, action, target, gmailPolicy string) (*GlobalSummary, error) {
	idsJSON, _ := json.Marshal(messageIDs)
	if gmailPolicy == "" {
		gmailPolicy = "skip"
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
`+jsResolveMailbox+`
const wanted = %s;
const action = '%s';
const target = '%s';
const gmailPolicy = '%s';
const results = [];
// Any container works for byId; the unified inbox is always present.
const container = mail.inbox().messages;
const archiveBoxes = {};
const quarantineBoxes = {};
const isGmail = {};
for (const id of wanted) {
	const r = { id: id, status: 'ok' };
	try {
		const n = Number(id);
		let mbox;
		try { mbox = container.byId(n).mailbox(); r.mailbox = mbox.name(); } catch (e) { r.status = 'missing'; results.push(r); continue; }
		// Commands like move need a specifier scoped to the message's own
		// mailbox; a byId reference through another container is rejected.
		const msg = mbox.messages.byId(n);
		const acc = mbox.account();
		const accName = acc.name();
		r.account = accName;
		if (isGmail[accName] === undefined) isGmail[accName] = !!resolveMailbox(acc, 'All Mail');
		r.gmail = isGmail[accName];
		switch (action) {
		case 'read': msg.readStatus = true; break;
		case 'unread': msg.readStatus = false; break;
		case 'flag': msg.flaggedStatus = true; break;
		case 'unflag': msg.flaggedStatus = false; break;
		case 'quarantine': {
			if (quarantineBoxes[accName] === undefined) quarantineBoxes[accName] = resolveMailbox(acc, target);
			const dest = quarantineBoxes[accName];
			if (!dest) throw 'Holding mailbox "' + target + '" is missing in ' + accName;
			mail.move(msg, { to: dest });
			break;
		}
		case 'move': {
			const dest = resolveMailbox(acc, target);
			if (!dest) throw 'Destination mailbox "' + target + '" not found in ' + accName;
			mail.move(msg, { to: dest });
			break;
		}
		case 'archive': {
			if (r.gmail) {
				if (gmailPolicy === 'read') { msg.readStatus = true; }
				else { r.status = 'skipped'; r.error = 'Gmail accounts cannot be archived via Mail.app scripting'; }
				break;
			}
			if (archiveBoxes[accName] === undefined) archiveBoxes[accName] = resolveMailbox(acc, 'Archive');
			if (!archiveBoxes[accName]) throw 'Archive mailbox not found in ' + accName;
			mail.move(msg, { to: archiveBoxes[accName] });
			break;
		}
		default: throw 'unknown action ' + action;
		}
	} catch (e) {
		r.status = 'failed'; r.error = String(e);
	}
	results.push(r);
}
JSON.stringify(results);
`, string(idsJSON), escapeJSString(action), escapeJSString(target), escapeJSString(gmailPolicy))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var results []GlobalResult
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		return nil, fmt.Errorf("unexpected output: %s", output)
	}
	s := &GlobalSummary{Results: results}
	for _, r := range results {
		switch r.Status {
		case "ok":
			s.OK++
		case "missing":
			s.Missing++
		case "failed":
			s.Failed++
		case "skipped":
			s.Skipped++
		}
	}
	return s, nil
}
