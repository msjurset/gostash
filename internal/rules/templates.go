package rules

import (
	"bytes"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

// TemplateData is the variable bag passed to every templated string action
// (set_title, set_note, append_note, notify). All fields are pre-populated
// even when empty so undefined references render to the empty string
// rather than an error.
//
// `Captures` exposes named regex capture groups from `url_regex` and
// `content_regex` match conditions. Reference them with
// `{{.Captures.fieldname}}`. If both regexes define the same name, the
// content_regex value wins (it runs last).
type TemplateData struct {
	ID           string
	Title        string
	URL          string
	Domain       string
	Type         string
	MimeType     string
	Sender       string
	SenderName   string
	SenderEmail  string
	SenderDomain string
	Subject      string
	Filename     string
	Date         string
	ExtractedText string
	// DuplicateOf is the existing item's full ID when the engine
	// detected a duplicate at capture time, otherwise empty. Use as
	// `{{.DuplicateOf}}` in link_to / set_note actions.
	DuplicateOf string
	// DuplicateOfShort is the same ID truncated to 8 chars for use
	// in human-readable contexts like add_tags or notify.
	DuplicateOfShort string
	Captures         map[string]string
	Rule             RuleContext
}

// RuleContext exposes a few rule-level fields under {{.Rule.Name}}.
type RuleContext struct {
	Name string
}

func buildTemplateData(item *model.Item, ruleName string, captures map[string]string) TemplateData {
	from := emailSender(item)
	td := TemplateData{
		ID:           item.ID,
		Title:        item.Title,
		URL:          item.URL,
		Domain:       urlHost(item.URL),
		Type:         string(item.Type),
		MimeType:     item.MimeType,
		Sender:       from,
		SenderName:   senderName(from),
		SenderEmail:  senderEmail(from),
		SenderDomain: senderDomain(from),
		Subject:      emailSubject(item),
		Date:         time.Now().UTC().Format("2006-01-02"),
		ExtractedText: item.ExtractedText,
		Captures:     captures,
		Rule:         RuleContext{Name: ruleName},
	}
	if item.SourcePath != "" {
		td.Filename = filepath.Base(item.SourcePath)
	}
	if td.Captures == nil {
		td.Captures = map[string]string{}
	}
	return td
}

// renderTemplate executes a Go text/template against TemplateData. Result
// is whitespace-trimmed. An empty rendered string is returned with no
// error — callers decide whether that means "skip this action" or "use
// the empty value".
//
// `missingkey=zero` makes references to absent map keys render as the
// empty string rather than failing — important for `{{.Captures.foo}}`
// where the name might not match any regex group.
func renderTemplate(tmpl string, data TemplateData) (string, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	t, err := template.New("rules").Funcs(template.FuncMap{
		"quote": func(s string) string {
			return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
		},
	}).Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}
