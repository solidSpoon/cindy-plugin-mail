package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func invoke(t *testing.T, args []string, stdin string) (int, map[string]any) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out)
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output is not JSON (%v): %s", err, out.String())
	}
	return code, doc
}

func TestVersion(t *testing.T) {
	code, doc := invoke(t, []string{"--version"}, "")
	if code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if doc["version"] != version {
		t.Errorf("version is %v, want %s", doc["version"], version)
	}
}

func TestHelpExplainsTheJSONProtocol(t *testing.T) {
	code, doc := invoke(t, []string{"--help"}, "")
	if code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if _, ok := doc["usage"]; !ok {
		t.Errorf("help should carry usage examples: %v", doc)
	}
	// The escaping is switched off, so the hint must be readable as typed.
	next, _ := doc["next"].(string)
	if !strings.Contains(next, `{"action":"schema"}`) {
		t.Errorf("help should point at the schema action, got %q", next)
	}
}

func TestEmptyInputPrintsHelpAndFails(t *testing.T) {
	code, doc := invoke(t, nil, "")
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if _, ok := doc["tool"]; !ok {
		t.Errorf("empty input should print the help document, got %v", doc)
	}
}

func TestMatchingAgainstASchemaWithoutMailApp(t *testing.T) {
	// draft with dryRun never touches Mail.app, so it is safe to run here.
	code, doc := invoke(t,
		[]string{`{"action":"draft","to":["a@x.com"],"subject":"s","body":"b","dryRun":true}`}, "")
	if code != 0 || doc["ok"] != true {
		t.Fatalf("dry-run draft failed: code=%d doc=%v", code, doc)
	}
}

func TestFailuresExitNonZeroAndStayJSON(t *testing.T) {
	for _, doc := range []string{`{"action":"nope"}`, `{`, `{"action":"messages.list","account":"A"}`} {
		code, out := invoke(t, []string{doc}, "")
		if code != 1 {
			t.Errorf("%s: exit code %d, want 1", doc, code)
		}
		if out["ok"] != false {
			t.Errorf("%s: ok should be false, got %v", doc, out["ok"])
		}
	}
}

func TestMoreThanOneArgumentIsRejected(t *testing.T) {
	code, doc := invoke(t, []string{`{"action":"schema"}`, `{"action":"schema"}`}, "")
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if doc["error"] == nil {
		t.Errorf("expected a document-level error, got %v", doc)
	}
}

func TestStdinInput(t *testing.T) {
	code, doc := invoke(t, nil, `{"action":"schema"}`)
	if code != 0 || doc["ok"] != true {
		t.Fatalf("stdin request failed: code=%d doc=%v", code, doc)
	}
}
