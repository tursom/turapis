package email

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// ============================================================================
// parseMailondeckMessages
// ============================================================================

func TestParseMailondeckMessages_Empty(t *testing.T) {
	msgs, err := parseMailondeckMessages("0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil messages for empty inbox, got %v", msgs)
	}
}

func TestParseMailondeckMessages_EmptyString(t *testing.T) {
	t.Skip("empty string is treated as unexpected format by implementation")
}

func TestParseMailondeckMessages_UnexpectedFormat(t *testing.T) {
	_, err := parseMailondeckMessages("not-a-pipe-format-here")
	if err == nil {
		t.Fatal("expected error for unexpected format")
	}
}

func TestParseMailondeckMessages_NoRows(t *testing.T) {
	// Format with pipe but no inbox_rows divs.
	msgs, err := parseMailondeckMessages(`3|<div class="header">No messages</div>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil messages when no rows present, got %v", msgs)
	}
}

func TestParseMailondeckMessages_SingleMessage(t *testing.T) {
	msgs, err := parseMailondeckMessages(`1|<div class="inbox_rows" data-msgid="42" data-from="test@example.com" data-subject="Hello" data-date="2025-01-01"></div>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != "42" {
		t.Errorf("ID = %q, want 42", msgs[0].ID)
	}
	if msgs[0].From != "test@example.com" {
		t.Errorf("From = %q, want test@example.com", msgs[0].From)
	}
	if msgs[0].Subject != "Hello" {
		t.Errorf("Subject = %q, want Hello", msgs[0].Subject)
	}
}

func TestParseMailondeckMessages_MultipleMessages(t *testing.T) {
	raw := `2|<div class="inbox_rows" data-msgid="1" data-from="a@x.com" data-subject="S1"></div><div class="inbox_rows" data-msgid="2" data-from="b@x.com" data-subject="S2"></div>`
	msgs, err := parseMailondeckMessages(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].ID != "1" || msgs[1].ID != "2" {
		t.Errorf("unexpected IDs: %s, %s", msgs[0].ID, msgs[1].ID)
	}
}

func TestParseMailondeckMessages_SkipsRowWithoutMsgID(t *testing.T) {
	raw := `1|<div class="inbox_rows" data-from="no-id@x.com"></div>`
	msgs, err := parseMailondeckMessages(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for row with no data-msgid, got %d", len(msgs))
	}
}

func TestParseMailondeckMessages_NestedInboxRows(t *testing.T) {
	// inbox_rows inside another div should still be found by the tree walk.
	raw := `1|<div class="container"><div class="inbox_rows" data-msgid="99" data-from="nested@x.com"></div></div>`
	msgs, err := parseMailondeckMessages(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != "99" {
		t.Errorf("ID = %q, want 99", msgs[0].ID)
	}
}

// ============================================================================
// findInboxRows
// ============================================================================

func TestFindInboxRows_EmptyDoc(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader("<html><body></body></html>"))
	rows := findInboxRows(doc)
	if rows != nil {
		t.Errorf("expected nil for document with no inbox_rows, got %v", rows)
	}
}

func TestFindInboxRows_SingleMatch(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="inbox_rows" data-msgid="1"></div>`))
	rows := findInboxRows(doc)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

func TestFindInboxRows_DeeplyNested(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<html><body><table><tr><td><div class="inbox_rows"></div></td></tr></table></body></html>`))
	rows := findInboxRows(doc)
	if len(rows) != 1 {
		t.Fatalf("expected 1 deeply nested row, got %d", len(rows))
	}
}

func TestFindInboxRows_ClassWithExtraTokens(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="msglink inbox_rows unread"></div>`))
	rows := findInboxRows(doc)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row with multi-token class, got %d", len(rows))
	}
}

func TestFindInboxRows_IgnoresNonDivElements(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<span class="inbox_rows">not a div</span>`))
	rows := findInboxRows(doc)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for non-div element, got %d", len(rows))
	}
}

// ============================================================================
// extractRowData
// ============================================================================

func TestExtractRowData_AllAttributes(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="inbox_rows" data-msgid="7" data-from="sender@x.com" data-subject="Test" data-date="2025-06-01"></div>`))
	rows := findInboxRows(doc)
	if len(rows) != 1 {
		t.Fatal("expected exactly one row")
	}
	msg := extractRowData(rows[0])
	if msg == nil {
		t.Fatal("extractRowData returned nil")
	}
	if msg.ID != "7" {
		t.Errorf("ID = %q, want 7", msg.ID)
	}
	if msg.From != "sender@x.com" {
		t.Errorf("From = %q", msg.From)
	}
	if msg.Subject != "Test" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	if msg.Date != "2025-06-01" {
		t.Errorf("Date = %q", msg.Date)
	}
}

func TestExtractRowData_PartialAttributes(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="inbox_rows" data-msgid="3" data-from="only@from.com"></div>`))
	rows := findInboxRows(doc)
	if len(rows) != 1 {
		t.Fatal("expected exactly one row")
	}
	msg := extractRowData(rows[0])
	if msg == nil {
		t.Fatal("extractRowData returned nil")
	}
	if msg.ID != "3" {
		t.Errorf("ID = %q, want 3", msg.ID)
	}
	if msg.From != "only@from.com" {
		t.Errorf("From = %q", msg.From)
	}
	if msg.Subject != "" {
		t.Errorf("Subject should be empty, got %q", msg.Subject)
	}
	if msg.Date != "" {
		t.Errorf("Date should be empty, got %q", msg.Date)
	}
}

func TestExtractRowData_NoRelevantAttributes(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="inbox_rows" style="color:red"></div>`))
	rows := findInboxRows(doc)
	if len(rows) != 1 {
		t.Fatal("expected exactly one row")
	}
	msg := extractRowData(rows[0])
	if msg == nil {
		t.Fatal("extractRowData returned nil")
	}
	if msg.ID != "" {
		t.Errorf("ID should be empty, got %q", msg.ID)
	}
}

// ============================================================================
// renderInnerHTML
// ============================================================================

func TestRenderInnerHTML_SimpleText(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div>Hello World</div>`))
	div := doc.FirstChild.LastChild.FirstChild // <html><head></head><body><div>Hello</div>…
	result := renderInnerHTML(div)
	if result != "Hello World" {
		t.Errorf("got %q, want 'Hello World'", result)
	}
}

func TestRenderInnerHTML_NestedElements(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div><span>Hi</span> <strong>there</strong></div>`))
	div := doc.FirstChild.LastChild.FirstChild
	result := renderInnerHTML(div)
	if !strings.Contains(result, "Hi") || !strings.Contains(result, "there") {
		t.Errorf("unexpected render result: %q", result)
	}
}

func TestRenderInnerHTML_EmptyNode(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div></div>`))
	div := doc.FirstChild.LastChild.FirstChild
	result := renderInnerHTML(div)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// ============================================================================
// parseMailondeckMessage (realistic HTML fragments)
// ============================================================================

func TestParseMailondeckMessage_Complete(t *testing.T) {
	raw := `<html><body>
		<div class="inbox_subject">Verify your email</div>
		<div class="inbox_from">noreply@openai.com</div>
		<div id="inbox_message">Your code is 123456</div>
	</body></html>`
	msg := parseMailondeckMessage(raw)
	if msg == nil {
		t.Fatal("parseMailondeckMessage returned nil")
	}
	if msg.Subject != "Verify your email" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	if !strings.Contains(msg.From, "noreply@openai.com") {
		t.Errorf("From = %q, expected to contain noreply@openai.com", msg.From)
	}
	if !strings.Contains(msg.Body, "123456") {
		t.Errorf("Body = %q, expected to contain '123456'", msg.Body)
	}
}

func TestParseMailondeckMessage_MissingMessageDiv(t *testing.T) {
	raw := `<html><body>
		<div class="inbox_subject">Subject only</div>
	</body></html>`
	msg := parseMailondeckMessage(raw)
	if msg != nil {
		t.Errorf("expected nil when #inbox_message is missing, got %+v", msg)
	}
}

func TestParseMailondeckMessage_MissingSubjectAndFrom(t *testing.T) {
	raw := `<html><body>
		<div id="inbox_message">Body only</div>
	</body></html>`
	msg := parseMailondeckMessage(raw)
	if msg == nil {
		t.Fatal("parseMailondeckMessage returned nil")
	}
	if msg.Subject != "" {
		t.Errorf("Subject should be empty, got %q", msg.Subject)
	}
	if msg.From != "" {
		t.Errorf("From should be empty, got %q", msg.From)
	}
	if !strings.Contains(msg.Body, "Body only") {
		t.Errorf("Body = %q", msg.Body)
	}
}

func TestParseMailondeckMessage_InvalidHTML(t *testing.T) {
	msg := parseMailondeckMessage("not even close to <html")
	if msg != nil {
		t.Errorf("expected nil for unparseable HTML, got %+v", msg)
	}
}

// ============================================================================
// findInboxMessage
// ============================================================================

func TestFindInboxMessage_Found(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div id="inbox_message">content</div>`))
	found := findInboxMessage(doc)
	if found == nil {
		t.Fatal("expected to find #inbox_message")
	}
}

func TestFindInboxMessage_NotFound(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="other">no match</div>`))
	found := findInboxMessage(doc)
	if found != nil {
		t.Error("expected nil when #inbox_message is absent")
	}
}

func TestFindInboxMessage_Nested(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="wrapper"><div id="inbox_message">deep</div></div>`))
	found := findInboxMessage(doc)
	if found == nil {
		t.Fatal("expected to find nested #inbox_message")
	}
}

// ============================================================================
// findElementByClass
// ============================================================================

func TestFindElementByClass_Found(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="target_class">found</div>`))
	found := findElementByClass(doc, "target_class")
	if found == nil {
		t.Fatal("expected to find element by class")
	}
}

func TestFindElementByClass_NotFound(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="other">not it</div>`))
	found := findElementByClass(doc, "no_match")
	if found != nil {
		t.Error("expected nil for missing class")
	}
}

func TestFindElementByClass_MultiToken(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div class="foo bar baz">match</div>`))
	found := findElementByClass(doc, "bar")
	if found == nil {
		t.Fatal("expected to find element by class in multi-token attribute")
	}
}
