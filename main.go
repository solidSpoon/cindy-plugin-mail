// Command mail-app-cli drives macOS Mail.app from a JSON request document.
//
// There is no flag interface: a request is a JSON object passed as the first
// argument or on stdin, and the answer is always a JSON envelope on stdout.
// Run it with {"action":"schema"} to get every action, its parameters and the
// tool's constraints.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/intelligrit/mail-app-cli/pkg/api"
)

const version = "2.0.1"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout))
}

func run(args []string, stdin io.Reader, stdout io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		writeJSON(stdout, helpDoc(), true)
		return 0
	case len(args) == 1 && (args[0] == "-v" || args[0] == "--version"):
		writeJSON(stdout, map[string]any{"tool": "mail-app-cli", "version": version}, true)
		return 0
	case len(args) > 1:
		writeJSON(stdout, api.Failure(api.CodeInvalidRequest,
			"expected one JSON document, got %d arguments", len(args)), true)
		return 1
	}

	var (
		doc []byte
		err error
	)
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		doc = []byte(args[0])
	} else {
		doc, err = io.ReadAll(stdin)
		if err != nil {
			writeJSON(stdout, api.Failure(api.CodeInvalidRequest, "could not read stdin: %v", err), true)
			return 1
		}
	}

	if len(bytes.TrimSpace(doc)) == 0 {
		writeJSON(stdout, helpDoc(), true)
		return 1
	}

	resp, opts := api.Execute(doc)
	writeJSON(stdout, resp, opts.Pretty)
	if resp.OK {
		return 0
	}
	return 1
}

// writeJSON prints the envelope. HTML escaping is off so that addresses read as
// <a@b.com> instead of \u003ca@b.com\u003e; indentation is opt-in because it
// roughly doubles the size of a list result.
func writeJSON(w io.Writer, v any, pretty bool) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "mail-app-cli: could not encode output: %v\n", err)
	}
}

// helpDoc is itself JSON, so even the help is machine readable.
func helpDoc() any {
	return map[string]any{
		"tool":    "mail-app-cli",
		"version": version,
		"usage": []string{
			`mail-app-cli '{"action":"schema"}'`,
			`mail-app-cli '{"action":"messages.unified","params":{"box":"unread","limit":10,"format":"compact"}}'`,
			`printf '%s' '{"ops":[...]}' | mail-app-cli`,
		},
		"input":       "one JSON request document, as the first argument or on stdin; an action plus its own params object, or an ops array of them",
		"output":      "a JSON envelope on stdout, single-line by default; add \"pretty\": true to indent it; exit code 0 when every operation succeeded, 1 otherwise",
		"constraints": []string{"no action sends mail", "no action deletes mail", "mutations accept dryRun:true"},
		"next":        `call {"action":"schema"} to list every action and its parameters`,
	}
}
