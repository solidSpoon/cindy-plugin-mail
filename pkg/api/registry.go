package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// action is one capability: an action name, the type of its parameter object
// and its handler. The parameter type is what makes the action and its inputs
// separable — the decoder and the schema both work from it, so a parameter can
// never exist for one and not the other.
type action struct {
	name       string
	summary    string
	paramsType reflect.Type
	newParams  func() any
	run        func(any) (any, error)
}

var (
	registry    = map[string]*action{}
	actionNames []string
)

// Register wires an action name to a parameter struct and a handler. Parameter
// structs document themselves with `desc`, `req:"1"`, `enum:"a|b"` and `def`
// tags; the schema reports them verbatim.
func Register[P any](name, summary string, run func(P) (any, error)) {
	if _, dup := registry[name]; dup {
		panic("api: duplicate action " + name)
	}
	registry[name] = &action{
		name:       name,
		summary:    summary,
		paramsType: reflect.TypeOf(*new(P)),
		newParams:  func() any { return new(P) },
		run: func(v any) (any, error) {
			return run(*(v.(*P)))
		},
	}
	actionNames = append(actionNames, name)
	sort.Strings(actionNames)
}

// operation is one action invocation. Parameters live in their own object so
// that the shape of an action's inputs is independent of the action itself.
type operation struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Pretty *bool           `json:"pretty,omitempty"`
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
		Action string            `json:"action"`
		Params json.RawMessage   `json:"params"`
		Pretty *bool             `json:"pretty"`
		Ops    []json.RawMessage `json:"ops"`
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
	var op operation
	dec := json.NewDecoder(bytes.NewReader(raw))
	// A stray parameter at this level is a caller that has not split params
	// out, so it is refused rather than ignored.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&op); err != nil {
		err := errf(CodeInvalidRequest, "operation is not valid: %v", err)
		if !bytes.Contains(raw, []byte(`"params"`)) {
			err = err.withHint(`parameters belong inside "params", e.g. {"action":"...","params":{...}}`)
		}
		return Result{Index: index, OK: false, Error: err}
	}

	if strings.TrimSpace(op.Action) == "" {
		return Result{Index: index, OK: false,
			Error: errf(CodeInvalidRequest, "operation has no action")}
	}
	a := registry[op.Action]
	if a == nil {
		return Result{Index: index, Action: op.Action, OK: false,
			Error: errf(CodeUnknownAction, "unknown action %q", op.Action).
				withHint(`call {"action":"schema"} for the full list`)}
	}

	res := Result{Index: index, Action: a.name}
	params := a.newParams()
	switch {
	case len(op.Params) == 0:
		if a.paramsType.NumField() > 0 {
			return Result{Index: index, Action: a.name, OK: false,
				Error: errf(CodeInvalidRequest, "%s requires a params object", a.name)}
		}
	default:
		pdec := json.NewDecoder(bytes.NewReader(op.Params))
		pdec.DisallowUnknownFields()
		if err := pdec.Decode(params); err != nil {
			return Result{Index: index, Action: a.name, OK: false,
				Error: errf(CodeInvalidRequest, "invalid params for %s: %v", a.name, err)}
		}
	}

	rv := reflect.ValueOf(params).Elem()
	if e := validateParams(a, rv, "params"); e != nil {
		return Result{Index: index, Action: a.name, DryRun: dryRunOf(rv), OK: false, Error: e}
	}
	res.DryRun = dryRunOf(rv)

	data, err := a.run(params)
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

func dryRunOf(rv reflect.Value) bool {
	if f := rv.FieldByName("DryRun"); f.IsValid() && f.Kind() == reflect.Bool {
		return f.Bool()
	}
	return false
}

// validateParams walks a parameter object, including nested objects and their
// elements, reporting the full path of the offending field.
func validateParams(a *action, rv reflect.Value, path string) *Error {
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if f.Anonymous && deref(f.Type).Kind() == reflect.Struct {
			if e := validateParams(a, derefValue(rv.Field(i)), path); e != nil {
				return e
			}
			continue
		}
		name := jsonNameOf(f)
		if name == "-" {
			continue
		}
		fieldPath := path + "." + name
		required := f.Tag.Get("req") == "1"
		fv := rv.Field(i)

		if fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				if required {
					return errf(CodeInvalidRequest, "%s: required parameter %q is missing", a.name, fieldPath)
				}
				continue
			}
			fv = fv.Elem()
		}

		switch fv.Kind() {
		case reflect.String:
			if required && strings.TrimSpace(fv.String()) == "" {
				return errf(CodeInvalidRequest, "%s: required parameter %q is missing", a.name, fieldPath)
			}
			if enum := splitList(f.Tag.Get("enum")); enum != nil && fv.String() != "" {
				if !contains(enum, fv.String()) {
					return errf(CodeInvalidRequest, "%s: %q must be one of %s (got %q)",
						a.name, fieldPath, strings.Join(enum, "|"), fv.String())
				}
			}
		case reflect.Slice:
			if required && fv.Len() == 0 {
				return errf(CodeInvalidRequest, "%s: required parameter %q is missing", a.name, fieldPath)
			}
			elem := deref(fv.Type().Elem())
			for j := 0; j < fv.Len(); j++ {
				item := derefValue(fv.Index(j))
				if enum := splitList(f.Tag.Get("enum")); enum != nil && item.Kind() == reflect.String {
					if !contains(enum, item.String()) {
						return errf(CodeInvalidRequest, "%s: %s[%d] must be one of %s (got %q)",
							a.name, fieldPath, j, strings.Join(enum, "|"), item.String())
					}
				}
				if elem.Kind() == reflect.Struct {
					if e := validateParams(a, item, fmt.Sprintf("%s[%d]", fieldPath, j)); e != nil {
						return e
					}
				}
			}
		case reflect.Struct:
			if e := validateParams(a, fv, fieldPath); e != nil {
				return e
			}
		}
	}
	return nil
}

func derefValue(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return v
		}
		v = v.Elem()
	}
	return v
}

func schemaData() any {
	return newBuilder().schema()
}
