package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// Field is one parameter, as reported by the `schema` action.
type Field struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Default  string   `json:"default,omitempty"`
	Desc     string   `json:"desc,omitempty"`
}

// action is one capability: a request struct type plus its handler.
type action struct {
	name    string
	summary string
	newReq  func() any
	run     func(any) (any, error)
	fields  []Field
}

var (
	registry    = map[string]*action{}
	actionNames []string
)

// Register wires a request type to a handler. Request structs embed `base` and
// document themselves with `desc` (prose), `req:"1"` (required), `enum:"a|b"`
// and `def` (default) tags; the `schema` action reports them verbatim, so the
// schema cannot drift from the decoder.
func Register[T any](name, summary string, run func(T) (any, error)) {
	if _, dup := registry[name]; dup {
		panic("api: duplicate action " + name)
	}
	t := reflect.TypeOf(*new(T))
	registry[name] = &action{
		name:    name,
		summary: summary,
		newReq:  func() any { return new(T) },
		run: func(v any) (any, error) {
			return run(*(v.(*T)))
		},
		fields: fieldsOf(t),
	}
	actionNames = append(actionNames, name)
	sort.Strings(actionNames)
}

// Options are document-level settings rather than operations.
type Options struct {
	// Pretty indents the output envelope. Off by default: indentation roughly
	// doubles the byte count of a list result for no information gain, and the
	// primary consumer here is a program.
	Pretty bool
}

// Execute handles one request document and returns the envelope to print.
func Execute(doc []byte) (Response, Options) {
	var probe struct {
		base
		Ops    []json.RawMessage `json:"ops"`
		Pretty *bool             `json:"pretty"`
	}
	opts := Options{}
	if err := json.Unmarshal(doc, &probe); err != nil {
		return Response{
			Results: []Result{},
			Error:   errf(CodeInvalidRequest, "input is not a JSON object: %v", err),
		}, opts
	}
	opts.Pretty = probe.Pretty != nil && *probe.Pretty

	raws := probe.Ops
	if len(raws) == 0 {
		if strings.TrimSpace(probe.Action) == "" {
			return Response{
				Results: []Result{},
				Error: errf(CodeInvalidRequest, `missing "action" (or an "ops" array)`).
					withHint(`call {"action":"schema"} to list the actions`),
			}, opts
		}
		raws = []json.RawMessage{doc}
	}

	resp := Response{OK: true, Results: make([]Result, 0, len(raws))}
	for i, raw := range raws {
		res := runOne(i, raw)
		if res.OK {
			resp.OKCount++
		} else {
			resp.Failed++
			resp.OK = false
		}
		resp.Results = append(resp.Results, res)
	}
	return resp, opts
}

func runOne(index int, raw json.RawMessage) Result {
	var head base
	if err := json.Unmarshal(raw, &head); err != nil {
		return Result{Index: index, OK: false,
			Error: errf(CodeInvalidRequest, "operation is not a JSON object: %v", err)}
	}
	a := registry[head.Action]
	if a == nil {
		return Result{Index: index, Action: head.Action, OK: false,
			Error: errf(CodeUnknownAction, "unknown action %q", head.Action).
				withHint(`call {"action":"schema"} for the full list`)}
	}

	res := Result{Index: index, Action: a.name}
	req := a.newReq()
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Rejecting unknown fields turns a misspelled parameter into an error
	// instead of a silently ignored default.
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		res.Error = errf(CodeInvalidRequest, "invalid parameters for %s: %v", a.name, err)
		return res
	}

	rv := reflect.ValueOf(req).Elem()
	if e := validate(a, rv); e != nil {
		res.Error = e
		return res
	}
	if f := rv.FieldByName("DryRun"); f.IsValid() && f.Kind() == reflect.Bool {
		res.DryRun = f.Bool()
	}

	data, err := a.run(req)
	if data != nil {
		// Data survives a partial failure so the caller can still see which ids
		// were handled and which were not.
		res.Data = data
	}
	if err != nil {
		res.Error = asError(err)
		return res
	}
	res.OK = true
	return res
}

func validate(a *action, rv reflect.Value) *Error {
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			continue
		}
		name := jsonNameOf(f)
		fv := rv.Field(i)
		if f.Tag.Get("req") == "1" {
			empty := (fv.Kind() == reflect.String && strings.TrimSpace(fv.String()) == "") ||
				(fv.Kind() == reflect.Slice && fv.Len() == 0)
			if empty {
				return errf(CodeInvalidRequest, "%s: required parameter %q is missing", a.name, name)
			}
		}
		if enum := f.Tag.Get("enum"); enum != "" && fv.Kind() == reflect.String {
			got := fv.String()
			if got == "" || contains(splitList(enum), got) {
				continue
			}
			return errf(CodeInvalidRequest, "%s: parameter %q must be one of %s (got %q)",
				a.name, name, enum, got)
		}
	}
	return nil
}

func schemaData() any {
	actions := make([]map[string]any, 0, len(actionNames))
	for _, name := range actionNames {
		a := registry[name]
		actions = append(actions, map[string]any{
			"name":    a.name,
			"summary": a.summary,
			"params":  a.fields,
		})
	}
	return map[string]any{
		"actions": actions,
		"constraints": []string{
			"no action sends mail: `draft` writes the message into Mail.app's Drafts mailbox for the user to review and send",
			"no action deletes mail: `messages.quarantine` moves messages into a holding mailbox, which is reversible from Mail.app",
			`every mutation accepts "dryRun": true, which reports what it would touch and changes nothing`,
			"mutations take all ids in one call; with no account/mailbox the ids are resolved globally because Mail.app message ids are unique database-wide",
			`list actions accept "format": "compact" to drop fields that are empty for the query` + "",
		},
		"input": []string{
			"pass the request document as the first argument or on stdin",
			`a document is either one operation ({"action":...}) or a batch ({"ops":[{"action":...},...]})`,
			`document level settings sit next to "action": "pretty": true indents the output`,
			"every operation reports ok/dryRun/data/error; unknown parameters are rejected rather than ignored",
			"exit code is 0 when every operation succeeded, 1 otherwise; the envelope is always printed",
		},
	}
}

func fieldsOf(t reflect.Type) []Field {
	fields := make([]Field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			continue
		}
		fields = append(fields, Field{
			Name:     jsonNameOf(f),
			Type:     goTypeOf(f.Type),
			Required: f.Tag.Get("req") == "1",
			Enum:     splitList(f.Tag.Get("enum")),
			Default:  f.Tag.Get("def"),
			Desc:     f.Tag.Get("desc"),
		})
	}
	return fields
}

func jsonNameOf(f reflect.StructField) string {
	name := strings.Split(f.Tag.Get("json"), ",")[0]
	if name == "" {
		name = f.Name
	}
	return name
}

func goTypeOf(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int64:
		return "int"
	case reflect.Slice:
		return goTypeOf(t.Elem()) + "[]"
	case reflect.Ptr:
		return goTypeOf(t.Elem())
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return t.Kind().String()
	}
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
