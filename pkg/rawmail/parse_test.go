package rawmail

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The fixtures are the shapes Mail.app's `source` actually hands over:
// multipart/alternative with a quoted-printable HTML part, a base64 part, and
// List-Unsubscribe headers.
const qpNewsletter = `Return-Path: <news@updates.example.com>
From: Example Updates <news@updates.example.com>
To: someone@example.com
Subject: =?utf-8?Q?Weekly?= digest
List-Unsubscribe: <https://updates.example.com/unsubscribe?token=abc>, <mailto:unsub@updates.example.com?subject=unsub>
List-Unsubscribe-Post: List-Unsubscribe=One-Click
Content-Type: multipart/alternative; boundary="BOUND"

--BOUND
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Weekly digest
You can instantly unsubscribe from emails of this kind
<https://updates.example.com/unsubscribe?token=3Dabc>
--BOUND
Content-Type: text/html; charset=utf-8
Content-Transfer-Encoding: quoted-printable

<html><body>
<p>Weekly digest</p>
<p><a href=3D"https://updates.example.com/unsubscribe?token=3Dabc&utm=3Dx">
instantly unsubscribe from emails of this kind</a></p>
<p><a href=3D'https://example.com/read'>Read it online</a></p>
<p><a href=3D"https://updates.example.com/unsubscribe?token=3Dabc&utm=3Dx">
instantly unsubscribe from emails of this kind</a></p>
</body></html>
--BOUND--
`

func TestParseMultipartAlternativeQuotedPrintable(t *testing.T) {
	body, err := Parse(qpNewsletter)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Text, "Weekly digest") {
		t.Errorf("plain text part missing: %q", body.Text)
	}
	if !strings.Contains(body.HTML, "<html>") {
		t.Errorf("html part missing: %q", body.HTML)
	}
	if len(body.Links) != 2 {
		t.Fatalf("got %d links, want 2 (exact duplicates dropped): %+v", len(body.Links), body.Links)
	}
	want := Link{
		Text: "instantly unsubscribe from emails of this kind",
		Href: "https://updates.example.com/unsubscribe?token=abc&utm=x",
	}
	if body.Links[0] != want {
		t.Errorf("link 0 = %+v, want %+v", body.Links[0], want)
	}
	if body.Links[1].Href != "https://example.com/read" {
		t.Errorf("single-quoted href not read: %+v", body.Links[1])
	}
	if len(body.ListUnsubscribe) != 2 {
		t.Fatalf("List-Unsubscribe = %v, want 2 entries", body.ListUnsubscribe)
	}
	if !body.ListUnsubscribeOneClick {
		t.Error("List-Unsubscribe-Post: One-Click should be reported")
	}
}

func TestParseBase64Part(t *testing.T) {
	htmlPart := `<html><body><a href="https://example.com/u">unsubscribe here</a></body></html>`
	raw := "Content-Type: multipart/related; boundary=\"B\"\n" +
		"Subject: base64 case\n\n" +
		"--B\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n\n" +
		base64.StdEncoding.EncodeToString([]byte("plain body")) + "\n" +
		"--B\nContent-Type: text/html; charset=utf-8\nContent-Transfer-Encoding: base64\n\n" +
		base64.StdEncoding.EncodeToString([]byte(htmlPart)) + "\n--B--\n"

	body, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "plain body" {
		t.Errorf("text = %q", body.Text)
	}
	if len(body.Links) != 1 || body.Links[0].Href != "https://example.com/u" {
		t.Errorf("links = %+v", body.Links)
	}
	if body.Links[0].Text != "unsubscribe here" {
		t.Errorf("anchor text = %q", body.Links[0].Text)
	}
}

func TestParseNestedMultipartAndMissingHTML(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=\"OUT\"\n\n" +
		"--OUT\nContent-Type: multipart/alternative; boundary=\"IN\"\n\n" +
		"--IN\nContent-Type: text/plain\n\nfrom the nested part\n" +
		"--IN\nContent-Type: text/html\n\n<p>no anchors here</p>\n" +
		"--IN--\n" +
		"--OUT\nContent-Type: application/pdf; name=\"x.pdf\"\n\n(binary)\n--OUT--\n"

	body, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Text, "from the nested part") {
		t.Errorf("nested text part missing: %q", body.Text)
	}
	if !strings.Contains(body.HTML, "no anchors here") {
		t.Errorf("nested html part missing: %q", body.HTML)
	}
	if len(body.Links) != 0 {
		t.Errorf("expected no links, got %+v", body.Links)
	}
	if len(body.ListUnsubscribe) != 0 {
		t.Errorf("expected no List-Unsubscribe, got %v", body.ListUnsubscribe)
	}
}

func TestParseMessageWithNoListUnsubscribe(t *testing.T) {
	raw := "From: a@b.com\nSubject: plain\nContent-Type: text/plain\n\nthe body\n"
	body, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "the body\n" {
		t.Errorf("text = %q", body.Text)
	}
	if body.HTML != "" || len(body.Links) != 0 || len(body.ListUnsubscribe) != 0 {
		t.Errorf("unexpected extras: %+v", body)
	}
}

func TestParseRejectsNonMessage(t *testing.T) {
	if _, err := Parse("not a message at all\n"); err == nil {
		t.Error("a source without headers should be an error")
	}
}

func TestExtractLinksEntitiesAndUntaggedText(t *testing.T) {
	doc := `<a href="https://a.example/?x=1&amp;y=2"><b>Go</b> &amp; <i>see</i></a>
	            <a>no href</a>
	            <a href="https://b.example/">  spaced   text </a>`
	links := ExtractLinks(doc)
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2: %+v", len(links), links)
	}
	if links[0].Href != "https://a.example/?x=1&y=2" {
		t.Errorf("entities not unescaped: %q", links[0].Href)
	}
	if links[0].Text != "Go & see" {
		t.Errorf("tag-stripped text = %q, want %q", links[0].Text, "Go & see")
	}
	if links[1].Text != "spaced text" {
		t.Errorf("whitespace not collapsed: %q", links[1].Text)
	}
}

func TestListUnsubscribeBareValue(t *testing.T) {
	entries, oneClick := listUnsubscribe(map[string][]string{
		"List-Unsubscribe": {"https://example.com/u"},
	})
	if len(entries) != 1 || entries[0] != "https://example.com/u" {
		t.Errorf("entries = %v", entries)
	}
	if oneClick {
		t.Error("one-click should be false without List-Unsubscribe-Post")
	}
}
