package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
)

// These tests deliberately cover only the parts that do not talk to Mail.app:
// request decoding, parameter validation, the schema and the dry-run paths.
// Anything that reaches Mail.app would need the user's live mailbox, so it is
// exercised manually instead.

func run(t *testing.T, doc string) (Response, Options) {
	t.Helper()
	return Execute([]byte(doc))
}

func firstError(t *testing.T, doc string) *Error {
	t.Helper()
	resp, _ := run(t, doc)
	if resp.OK {
		t.Fatalf("%s should not have succeeded", doc)
	}
	if len(resp.Results) == 0 || resp.Results[0].Error == nil {
		t.Fatalf("%s produced no operation error: %+v", doc, resp)
	}
	return resp.Results[0].Error
}

func schemaOf(t *testing.T) Schema {
	t.Helper()
	resp, _ := run(t, `{"action":"schema"}`)
	if !resp.OK {
		t.Fatalf("schema failed: %+v", resp)
	}
	raw, err := json.Marshal(resp.Results[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	var schema Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestSchemaDescribesEveryAction(t *testing.T) {
	schema := schemaOf(t)
	if len(schema.Actions) != len(actionNames) {
		t.Fatalf("schema has %d actions, registry has %d", len(schema.Actions), len(actionNames))
	}
	for _, a := range schema.Actions {
		if a.Name == "" || a.Summary == "" {
			t.Errorf("incomplete action spec: %+v", a)
		}
		props, ok := a.Params["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s has no params properties: %+v", a.Name, a.Params)
			continue
		}
		if len(props) == 0 && a.Name != "schema" {
			t.Errorf("%s documents no parameters", a.Name)
		}
	}
	for _, want := range []string{"schema", "messages.list", "messages.patch", "messages.quarantine", "draft"} {
		if registry[want] == nil {
			t.Errorf("action %q is not registered", want)
		}
	}
}

func TestSchemaNeverAdvertisesSendingOrDeleting(t *testing.T) {
	for _, name := range actionNames {
		if strings.Contains(name, "delete") {
			t.Errorf("action %q looks like a delete; mail is quarantined, never deleted", name)
		}
		if strings.Contains(name, "send") {
			t.Errorf("action %q looks like a send; drafts are composed for the user to send", name)
		}
	}
}

func TestSchemaSharesNestedTypesUnderDefs(t *testing.T) {
	schema := schemaOf(t)
	for _, want := range []string{"MailboxRef", "MessageInMailbox", "Recipients", "StateSet"} {
		if _, ok := schema.Defs[want]; !ok {
			t.Errorf("def %q is missing; shared parameter types should be described once", want)
		}
	}
	ref := schema.Defs["MailboxRef"]
	if ref.Required == nil || len(ref.Required) != 2 {
		t.Errorf("MailboxRef should require both fields, got %+v", ref)
	}
	// An action that takes a mailbox must point at the def rather than repeat
	// its fields.
	var list ActionSpec
	for _, a := range schema.Actions {
		if a.Name == "messages.list" {
			list = a
		}
	}
	props := list.Params["properties"].(map[string]any)
	box := props["box"].(map[string]any)
	if box["type"] != "string" {
		t.Errorf("messages.list.box should be a string, got %+v", box)
	}
	if _, ok := box["enum"]; !ok {
		t.Errorf("messages.list.box should carry its view enum, got %+v", box)
	}
	account := props["account"].(map[string]any)
	if account["type"] != "string" {
		t.Errorf("messages.list.account should be a string, got %+v", account)
	}
	if account["desc"] == "" {
		t.Errorf("messages.list.account should carry its description")
	}

	var show ActionSpec
	for _, a := range schema.Actions {
		if a.Name == "messages.show" {
			show = a
		}
	}
	message := show.Params["properties"].(map[string]any)["message"].(map[string]any)
	if message["ref"] != "MessageInMailbox" {
		t.Errorf("messages.show.message should reference MessageInMailbox, got %+v", message)
	}
}

func TestExecuteSingleOperation(t *testing.T) {
	resp, opts := run(t, `{"action":"draft","params":{"recipients":{"to":["a@x.com"]},"content":{"subject":"hi","body":"b"},"dryRun":true},"pretty":true}`)
	if !resp.OK || resp.OKCount != 1 || resp.Failed != 0 {
		t.Fatalf("unexpected envelope: %+v", resp)
	}
	if !opts.Pretty {
		t.Error("pretty should be honoured")
	}
	if !resp.Results[0].DryRun {
		t.Error("dryRun should be reported on the result")
	}
}

func TestParametersMustBeNestedUnderParams(t *testing.T) {
	// The pre-split form: parameters sitting next to the action.
	e := firstError(t, `{"action":"messages.list","account":"Exchange","mailbox":"收件箱"}`)
	if e.Code != CodeInvalidRequest {
		t.Errorf("code is %q, want %q", e.Code, CodeInvalidRequest)
	}
	if !strings.Contains(e.Hint, "params") {
		t.Errorf("hint should point at the params wrapper, got %q", e.Hint)
	}
}

func TestMissingParamsObjectIsReported(t *testing.T) {
	e := firstError(t, `{"action":"messages.list"}`)
	if !strings.Contains(e.Message, "requires a params object") {
		t.Errorf("unexpected message: %q", e.Message)
	}
}

func TestNestedRequiredFieldsAreNamed(t *testing.T) {
	e := firstError(t, `{"action":"messages.show","params":{"message":{"id":"7847"}}}`)
	if e.Code != CodeInvalidRequest {
		t.Fatalf("code is %q, want %q", e.Code, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "params.message.in") {
		t.Errorf("error should give the nested path, got %q", e.Message)
	}
}

func TestUnknownNestedParameterIsRejected(t *testing.T) {
	e := firstError(t, `{"action":"messages.show","params":{"message":{"id":"1","in":{"account":"A","mailbox":"B","bogus":1}}}}`)
	if !strings.Contains(e.Message, "bogus") {
		t.Errorf("error should name the unknown field, got %q", e.Message)
	}
}

func TestListTargetMustBeUnambiguous(t *testing.T) {
	// A box view and a named mailbox are alternatives, not a combination.
	e := firstError(t, `{"action":"messages.list","params":{"box":"unread","account":"Exchange","mailbox":"收件箱"}}`)
	if !strings.Contains(e.Message, "cannot be combined") {
		t.Errorf("box plus account/mailbox should be rejected, got %q", e.Message)
	}

	// The filters belong to the named-mailbox form; a view already filters.
	e = firstError(t, `{"action":"messages.list","params":{"box":"unread","unread":true}}`)
	if !strings.Contains(e.Message, "account+mailbox only") {
		t.Errorf("unread with box should be rejected, got %q", e.Message)
	}

	e = firstError(t, `{"action":"messages.list","params":{"limit":5}}`)
	if !strings.Contains(e.Message, "give box, or both account and mailbox") {
		t.Errorf("neither target should be rejected, got %q", e.Message)
	}
}

func TestBoxViewEnumIsValidated(t *testing.T) {
	e := firstError(t, `{"action":"messages.list","params":{"box":"spam"}}`)
	if !strings.Contains(e.Message, "inbox|unread|flagged|sent|drafts|trash|junk") {
		t.Errorf("box should be validated against its enum, got %q", e.Message)
	}
}

func TestUnknownActionAndUnknownField(t *testing.T) {
	if e := firstError(t, `{"action":"nope","params":{}}`); e.Code != CodeUnknownAction {
		t.Errorf("code is %q, want %q", e.Code, CodeUnknownAction)
	}
	e := firstError(t, `{"action":"messages.list","params":{"account":"A","mailbox":"B","limt":5}}`)
	if e.Code != CodeInvalidRequest || !strings.Contains(e.Message, "limt") {
		t.Errorf("misspelled parameter should fail by name, got %+v", e)
	}
}

func TestPatchRequiresSomethingToSet(t *testing.T) {
	// The dryRun check comes after validation, so this never reaches Mail.app.
	e := firstError(t, `{"action":"messages.patch","params":{"ids":["1"],"set":{}}}`)
	if !strings.Contains(e.Message, "read and/or flagged") {
		t.Errorf("empty set should be rejected, got %q", e.Message)
	}
}

func TestExecuteBatchKeepsGoingAfterAFailure(t *testing.T) {
	doc := `{"ops":[
		{"action":"draft","params":{"recipients":{"to":["a@x.com"]},"content":{"subject":"s"},"dryRun":true}},
		{"action":"nope","params":{}},
		{"action":"draft","params":{"recipients":{"to":["b@x.com"]},"content":{"subject":"s2"},"dryRun":true}}
	]}`
	resp, _ := run(t, doc)
	if resp.OK {
		t.Error("a batch with a failing operation must not report ok")
	}
	if resp.OKCount != 2 || resp.Failed != 1 {
		t.Errorf("okCount=%d failed=%d, want 2/1", resp.OKCount, resp.Failed)
	}
	if got := resp.Results[1].Error.Code; got != CodeUnknownAction {
		t.Errorf("middle operation code is %q, want %q", got, CodeUnknownAction)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(resp.Results))
	}
}

func TestMalformedDocument(t *testing.T) {
	for _, doc := range []string{`{`, `[]`, `"hi"`, `{"ops":[]}`} {
		resp, _ := run(t, doc)
		if resp.OK {
			t.Errorf("%q should not have succeeded", doc)
		}
		if resp.Error == nil {
			t.Errorf("%q: document-level failures belong in the top-level error", doc)
		}
	}
}

func TestPartialFailureKeepsPerIDData(t *testing.T) {
	res := &mail.MutationResult{
		Succeeded: []string{"1"},
		Missing:   []string{"2"},
		Failed:    map[string]string{"3": "boom"},
	}
	data, err := mutationData(res, "marked read")
	if err == nil {
		t.Fatal("a partial failure must return an error")
	}
	if data == nil {
		t.Fatal("data must survive so the caller still sees which ids worked")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodePartialFailure {
		t.Errorf("error is %v, want %s", err, CodePartialFailure)
	}
}

// TestCompactProjectionShape pins the triage projection: the fields an agent
// needs to decide, and none of the ones that are empty for a list query.
func TestCompactProjectionShape(t *testing.T) {
	projected, err := json.Marshal(projectMessages([]mail.Message{
		{ID: "1", Account: "A", Mailbox: "Inbox", DateReceived: "2026-01-01T00:00:00Z",
			Sender: "s", Subject: "sub", Read: true, Flagged: true},
	}, "compact"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(projected, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	for _, wanted := range []string{"id", "account", "mailbox", "date", "read", "flagged", "from", "subject"} {
		if _, ok := rows[0][wanted]; !ok {
			t.Errorf("compact row is missing %q: %s", wanted, projected)
		}
	}
	if len(rows[0]) != 8 {
		t.Errorf("compact row has %d fields, want 8: %s", len(rows[0]), projected)
	}

	full := projectMessages([]mail.Message{{ID: "1", Content: "body"}}, "full")
	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Content"`) {
		t.Errorf("full projection lost fields: %s", raw)
	}
}

func TestCombineMutationError(t *testing.T) {
	reloc := errf(CodeMoveNotPersisted, "not persisted")
	partial := errf(CodePartialFailure, "partial")

	if got := combineMutationError(reloc, partial); got != reloc {
		t.Errorf("relocation error should win over partial failure, got %v", got)
	}
	if got := combineMutationError(nil, partial); got != partial {
		t.Errorf("partial failure must not be swallowed when relocation succeeded, got %v", got)
	}
	if got := combineMutationError(nil, nil); got != nil {
		t.Errorf("expected nil when both errors are nil, got %v", got)
	}
}

func TestDefaultQuarantineBoxEnv(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_QUARANTINE_BOX", "")
	if got := defaultQuarantineBox(); got != mail.DefaultQuarantineMailbox {
		t.Errorf("got %q, want %q", got, mail.DefaultQuarantineMailbox)
	}
	t.Setenv("MAIL_APP_CLI_QUARANTINE_BOX", "  Quarantine  ")
	if got := defaultQuarantineBox(); got != "Quarantine" {
		t.Errorf("got %q, want %q", got, "Quarantine")
	}
}

func TestPartitionLocations(t *testing.T) {
	locs := []mail.MessageLocation{
		{ID: "1", Exists: true, Account: "A", Mailbox: "INBOX"},
		{ID: "2", Exists: false},
	}
	wouldAffect, missing := partitionLocations(locs)
	if len(wouldAffect) != 1 || wouldAffect[0].ID != "1" {
		t.Errorf("unexpected wouldAffect: %+v", wouldAffect)
	}
	if len(missing) != 1 || missing[0] != "2" {
		t.Errorf("unexpected missing: %+v", missing)
	}
}

func TestSchemaIncludesQuarantineMarkRead(t *testing.T) {
	schema := schemaOf(t)
	for _, a := range schema.Actions {
		if a.Name == "messages.quarantine" {
			props := a.Params["properties"].(map[string]any)
			if _, ok := props["markRead"]; !ok {
				t.Errorf("messages.quarantine should expose markRead in schema")
			}
			return
		}
	}
	t.Fatal("messages.quarantine not found in schema")
}

func TestFilterMessagesByQueryAndPaginate(t *testing.T) {
	msgs := []mail.Message{
		{ID: "1", Subject: "Weekly Digest", Sender: "news@acme.com"},
		{ID: "2", Subject: "Invoice #42", Sender: "Billing <bill@vendor.com>"},
		{ID: "3", Subject: "Acme promo", Sender: "noreply@promo.io"},
	}
	filtered := filterMessagesByQuery(msgs, "ACME")
	if len(filtered) != 2 || filtered[0].ID != "1" || filtered[1].ID != "3" {
		t.Fatalf("unexpected query filter result: %+v", filtered)
	}
	page, hasMore := paginateMessages(filtered, 0, 1)
	if len(page) != 1 || page[0].ID != "1" || !hasMore {
		t.Fatalf("unexpected page 1: %+v hasMore=%v", page, hasMore)
	}
	page2, hasMore2 := paginateMessages(filtered, 1, 1)
	if len(page2) != 1 || page2[0].ID != "3" || hasMore2 {
		t.Fatalf("unexpected page 2: %+v hasMore=%v", page2, hasMore2)
	}
}

func TestBuildDigestGroupsAndSorts(t *testing.T) {
	msgs := []mail.Message{
		{ID: "1", Sender: "A <a@x.com>", Subject: "S1", DateReceived: "2026-11-17T10:00:00Z", Read: false},
		{ID: "2", Sender: "B <b@x.com>", Subject: "B1", DateReceived: "2026-11-17T11:00:00Z", Read: true},
		{ID: "3", Sender: "a <a@x.com>", Subject: "S2", DateReceived: "2026-11-17T12:00:00Z", Read: false},
		{ID: "4", Sender: "A <a@x.com>", Subject: "S3", DateReceived: "2026-11-17T13:00:00Z", Read: true},
		{ID: "5", Sender: "A <a@x.com>", Subject: "S4", DateReceived: "2026-11-17T14:00:00Z", Read: false},
	}
	groups := buildDigest(msgs)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].Count != 4 || groups[0].UnreadCount != 3 || len(groups[0].Subjects) != 3 || groups[0].Newest != "2026-11-17T14:00:00Z" {
		t.Fatalf("unexpected top group: %+v", groups[0])
	}
	if groups[1].Count != 1 {
		t.Fatalf("unexpected second group: %+v", groups[1])
	}
}

func TestDigestTargetValidation(t *testing.T) {
	e := firstError(t, `{"action":"messages.digest","params":{"box":"inbox","account":"Google","mailbox":"INBOX"}}`)
	if !strings.Contains(e.Message, "cannot be combined") {
		t.Errorf("expected mutual exclusion error, got %q", e.Message)
	}
	e = firstError(t, `{"action":"messages.digest","params":{"limit":10}}`)
	if !strings.Contains(e.Message, "give box, or both account and mailbox") {
		t.Errorf("expected missing target error, got %q", e.Message)
	}
}

func TestStillInSourceVerification(t *testing.T) {
	if !isAllMailMailbox("All Mail") || !isAllMailMailbox("[Gmail]/All Mail") || isAllMailMailbox("INBOX") {
		t.Fatal("unexpected isAllMailMailbox classification")
	}
	rep := &moveReport{Verified: []string{"1"}, StillInSource: []string{"1"}}
	err := relocationError(rep, "待删除")
	if err == nil {
		t.Fatal("expected relocationError when StillInSource is non-empty")
	}
	if apiErr, ok := err.(*Error); !ok || apiErr.Code != CodeMoveNotPersisted {
		t.Fatalf("expected %s, got %v", CodeMoveNotPersisted, err)
	}
	data := map[string]any{}
	attachReport(data, rep)
	if data["leftSource"] != false {
		t.Errorf("expected leftSource=false, got %v", data["leftSource"])
	}
}

func TestSchemaListDefaultLimitIs60(t *testing.T) {
	schema := schemaOf(t)
	for _, a := range schema.Actions {
		if a.Name == "messages.list" {
			props := a.Params["properties"].(map[string]any)
			lim := props["limit"].(map[string]any)
			if lim["default"] != "60" {
				t.Errorf("messages.list.limit default = %v, want 60", lim["default"])
			}
			if _, ok := props["query"]; !ok {
				t.Errorf("messages.list should expose query in schema")
			}
			return
		}
	}
	t.Fatal("messages.list not found in schema")
}
