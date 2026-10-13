package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
)

// These tests deliberately cover only the parts that do not talk to Mail.app:
// request decoding, validation, the schema and the dry-run paths. Anything that
// reaches Mail.app would need the user's live mailbox, so it is not exercised
// here.

func run(t *testing.T, doc string) (Response, Options) {
	t.Helper()
	return Execute([]byte(doc))
}

func TestSchemaDescribesEveryAction(t *testing.T) {
	resp, _ := run(t, `{"action":"schema"}`)
	if !resp.OK || len(resp.Results) != 1 {
		t.Fatalf("schema did not run: %+v", resp)
	}
	data, ok := resp.Results[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("schema data is %T, want object", resp.Results[0].Data)
	}
	actions, ok := data["actions"].([]map[string]any)
	if !ok {
		t.Fatalf("actions is %T, want a slice", data["actions"])
	}
	if len(actions) != len(actionNames) {
		t.Fatalf("schema has %d actions, registry has %d", len(actions), len(actionNames))
	}
	for _, a := range actions {
		name, _ := a["name"].(string)
		if name == "" {
			t.Errorf("action without a name: %+v", a)
		}
		if summary, _ := a["summary"].(string); summary == "" {
			t.Errorf("%s has no summary", name)
		}
		if _, ok := a["params"].([]Field); !ok {
			t.Errorf("%s params are %T, want []Field", name, a["params"])
		}
	}
	for _, want := range []string{"schema", "messages.list", "messages.quarantine", "draft"} {
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

func TestSchemaReportsRequiredAndEnumParams(t *testing.T) {
	fields := map[string]Field{}
	for _, f := range registry["messages.list"].fields {
		fields[f.Name] = f
	}
	for _, name := range []string{"account", "mailbox"} {
		if !fields[name].Required {
			t.Errorf("messages.list.%s should be required", name)
		}
	}
	if fields["format"].Enum == nil || !contains(fields["format"].Enum, "compact") {
		t.Errorf("messages.list.format should advertise its enum, got %+v", fields["format"])
	}
	if fields["limit"].Default != "25" {
		t.Errorf("messages.list.limit default is %q, want 25", fields["limit"].Default)
	}
	if got := fields["unread"].Type; got != "bool" {
		t.Errorf("messages.list.unread type is %q, want bool", got)
	}

	mark := map[string]Field{}
	for _, f := range registry["messages.mark"].fields {
		mark[f.Name] = f
	}
	// read is a *bool with a default, to tell "unset" from "false".
	if mark["read"].Type != "bool" || mark["read"].Default != "true" {
		t.Errorf("messages.mark.read is %+v, want an optional bool defaulting to true", mark["read"])
	}
}

func TestExecuteSingleOperation(t *testing.T) {
	resp, opts := run(t, `{"action":"draft","account":"Google","to":["a@x.com"],"subject":"hi","body":"b","dryRun":true,"pretty":true}`)
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

func TestExecuteBatchKeepsGoingAfterAFailure(t *testing.T) {
	doc := `{"ops":[
		{"action":"draft","to":["a@x.com"],"subject":"s","dryRun":true},
		{"action":"nope"},
		{"action":"draft","to":["b@x.com"],"subject":"s2","dryRun":true}
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

func TestUnknownParameterIsRejected(t *testing.T) {
	resp, _ := run(t, `{"action":"messages.list","account":"A","mailbox":"B","limt":5}`)
	if resp.OK {
		t.Fatal("a misspelled parameter must fail rather than be ignored")
	}
	if got := resp.Results[0].Error.Code; got != CodeInvalidRequest {
		t.Errorf("code is %q, want %q", got, CodeInvalidRequest)
	}
	if !strings.Contains(resp.Results[0].Error.Message, "limt") {
		t.Errorf("message should name the offending field: %q", resp.Results[0].Error.Message)
	}
}

func TestMissingRequiredParameterIsNamedInTheError(t *testing.T) {
	resp, _ := run(t, `{"action":"messages.list","account":"A"}`)
	if resp.OK {
		t.Fatal("missing mailbox must fail")
	}
	msg := resp.Results[0].Error.Message
	if !strings.Contains(msg, `"mailbox"`) {
		t.Errorf("message should name the missing field: %q", msg)
	}
}

func TestEnumParameterIsValidated(t *testing.T) {
	resp, _ := run(t, `{"action":"messages.unified","box":"everything"}`)
	if resp.OK {
		t.Fatal("an out-of-enum box must fail")
	}
	if !strings.Contains(resp.Results[0].Error.Message, "inbox|unread") {
		t.Errorf("message should list the allowed values: %q", resp.Results[0].Error.Message)
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

	// The full projection must stay lossless.
	full := projectMessages([]mail.Message{{ID: "1", Content: "body"}}, "full")
	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Content"`) {
		t.Errorf("full projection lost fields: %s", raw)
	}
}
