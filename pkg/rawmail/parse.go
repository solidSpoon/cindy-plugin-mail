// Package rawmail parses the raw RFC822 source of a message.
//
// Mail.app's `content` property returns the *rendered* body, in which every
// hyperlink has already been flattened to its anchor text. The `source`
// property returns the message as it arrived, so the text/html part, its links
// and the List-Unsubscribe headers are all still there. This package turns that
// raw source into the pieces a caller actually wants.
package rawmail

import (
	"encoding/base64"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
)

// Link is one anchor in the HTML part: what the reader sees, and where it goes.
type Link struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

// Body is the parsed result.
type Body struct {
	Text  string `json:"text"`
	HTML  string `json:"html"`
	Links []Link `json:"links"`
	// ListUnsubscribe is the List-Unsubscribe header (RFC 2369) split into its
	// entries, which are usually an https URL and a mailto URL. It is the
	// sender's own machine-readable unsubscribe route, so it is more reliable
	// than scraping the body - mail with no anchor at all often still has it.
	ListUnsubscribe []string `json:"listUnsubscribe,omitempty"`
	// ListUnsubscribeOneClick reports RFC 8058's List-Unsubscribe-Post, meaning
	// the https entry accepts a bare POST with no confirmation page.
	ListUnsubscribeOneClick bool `json:"listUnsubscribeOneClick,omitempty"`
}

// Parse reads a raw message source. A source that cannot be parsed as a message
// at all is an error; a message with no text/html part simply yields an empty
// HTML field.
func Parse(raw string) (*Body, error) {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}
	b := &Body{}
	b.ListUnsubscribe, b.ListUnsubscribeOneClick = listUnsubscribe(msg.Header)
	if err := walk(mail.Header(msg.Header), msg.Body, b, 0); err != nil {
		return nil, err
	}
	if b.HTML != "" {
		b.Links = ExtractLinks(b.HTML)
	}
	return b, nil
}

// walk descends the MIME tree, keeping the first text/plain and the first
// text/html part it finds. Depth is bounded because a malformed message can
// otherwise nest without end.
func walk(h mail.Header, r io.Reader, b *Body, depth int) error {
	if depth > 12 {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil
		}
		mr := multipart.NewReader(r, boundary)
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := walk(mail.Header(part.Header), part, b, depth+1); err != nil {
				return err
			}
		}
	}

	switch mediaType {
	case "text/plain":
		if b.Text == "" {
			b.Text = decodeBody(h.Get("Content-Transfer-Encoding"), r)
		}
	case "text/html":
		if b.HTML == "" {
			b.HTML = decodeBody(h.Get("Content-Transfer-Encoding"), r)
		}
	}
	return nil
}

// decodeBody undoes the transfer encoding of one part. Anything unexpected is
// returned as-is rather than failing the whole parse.
func decodeBody(encoding string, r io.Reader) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		data, err := io.ReadAll(quotedprintable.NewReader(r))
		if err != nil {
			return ""
		}
		return string(data)
	case "base64":
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, r))
		if err != nil && len(data) == 0 {
			return ""
		}
		return string(data)
	default:
		data, err := io.ReadAll(r)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

var (
	anchorRe = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a\s*>`)
	hrefRe   = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// ExtractLinks pulls every anchor out of an HTML document, in document order,
// dropping exact duplicates. It is a scanner rather than a full HTML parser:
// enough for "which line do I click to unsubscribe", and it never rewrites the
// HTML it reads.
func ExtractLinks(document string) []Link {
	var links []Link
	seen := map[string]bool{}
	for _, m := range anchorRe.FindAllStringSubmatch(document, -1) {
		attrs, inner := m[1], m[2]
		href := ""
		if hm := hrefRe.FindStringSubmatch(attrs); hm != nil {
			href = firstNonEmpty(hm[1], hm[2], hm[3])
		}
		href = html.UnescapeString(strings.TrimSpace(href))
		text := html.UnescapeString(tagRe.ReplaceAllString(inner, " "))
		text = strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
		if href == "" {
			continue
		}
		key := href + "\x00" + text
		if seen[key] {
			continue
		}
		seen[key] = true
		links = append(links, Link{Text: text, Href: href})
	}
	return links
}

// listUnsubscribe returns the entries of List-Unsubscribe and whether the
// message advertises RFC 8058 one-click unsubscribing.
func listUnsubscribe(h map[string][]string) ([]string, bool) {
	oneClick := false
	for _, v := range headerValues(h, "List-Unsubscribe-Post") {
		if strings.Contains(strings.ToLower(v), "one-click") {
			oneClick = true
		}
	}

	var entries []string
	seen := map[string]bool{}
	for _, v := range headerValues(h, "List-Unsubscribe") {
		// Entries are angle-bracketed and comma separated: <https://…>, <mailto:…>
		for _, m := range regexp.MustCompile(`<([^>]+)>`).FindAllStringSubmatch(v, -1) {
			entry := strings.TrimSpace(m[1])
			if entry != "" && !seen[entry] {
				seen[entry] = true
				entries = append(entries, entry)
			}
		}
		if len(entries) == 0 {
			if plain := strings.TrimSpace(v); plain != "" && !seen[plain] {
				seen[plain] = true
				entries = append(entries, plain)
			}
		}
	}
	return entries, oneClick
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// headerValues looks a header up regardless of how its key was cased.
func headerValues(h map[string][]string, key string) []string {
	if vs, ok := h[key]; ok {
		return vs
	}
	canonical := textproto.CanonicalMIMEHeaderKey(key)
	if vs, ok := h[canonical]; ok {
		return vs
	}
	for k, vs := range h {
		if strings.EqualFold(k, key) {
			return vs
		}
	}
	return nil
}
