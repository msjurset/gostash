package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/audit"
	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"
	"github.com/spf13/cobra"
)

// ProvenanceEvent is one row in an item's timeline. Wire-friendly
// shape consumed by the Mac app's ItemDetailView provenance section
// and by anyone scripting against `stash provenance <id> --json`.
type ProvenanceEvent struct {
	Timestamp time.Time `json:"timestamp"`
	// Kind discriminates the event source:
	//   "capture"  — the original ingest (synthesized from the item
	//                record + matching capture.log entry if present)
	//   "rule"     — a rule fired against this item (capture.log)
	//   "skip"     — a rule matched but was skipped at capture time
	//   "tag"      — a manual tag add/remove (tags.log)
	//   "error"    — a capture-time error event (capture.log)
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// Optional supplementary fields. Empty when not applicable; the
	// Mac side decides whether to render badges / links from these.
	Source  string   `json:"source,omitempty"`  // ingest surface or edit surface
	Rule    string   `json:"rule,omitempty"`    // rule name on rule/skip events
	Effects []string `json:"effects,omitempty"` // rule effects ("tag:foo", etc.)
	Tag     string   `json:"tag,omitempty"`     // for tag events
	Action  string   `json:"action,omitempty"`  // "add" | "remove" for tag events
	URL     string   `json:"url,omitempty"`     // capture event: item URL
	Domain  string   `json:"domain,omitempty"`  // capture event: URL host
	Error   string   `json:"error,omitempty"`   // error event
}

var provenanceCmd = &cobra.Command{
	Use:   "provenance <id>",
	Short: "Show the captured-→-rule-→-tag history for one item",
	Long: `Render the lifecycle of a single stashed item as a chronological
timeline: how it was captured (and from where), which rules fired or
were skipped at capture time, and every subsequent manual tag
mutation. Powers the "Why is this here?" section in the Mac app's
detail pane.

Data is assembled from three sources:
  - the item record itself (created_at, source path / URL, type)
  - $STASH_DIR/capture.log (rule fires, skips, captures, errors)
  - $STASH_DIR/tags.log    (manual tag add/remove events)`,
	Args: cobra.ExactArgs(1),
	RunE: runProvenance,
}

func init() {
	rootCmd.AddCommand(provenanceCmd)
}

func runProvenance(_ *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}

	events, err := assembleProvenance(item)
	if err != nil {
		return err
	}

	if flagJSON {
		printJSONSlice(events)
		return nil
	}
	if len(events) == 0 {
		fmt.Println("No provenance recorded for this item.")
		return nil
	}
	for _, ev := range events {
		stamp := ev.Timestamp.Local().Format("2006-01-02 15:04")
		fmt.Printf("%s  %s\n", stamp, ev.Summary)
	}
	return nil
}

// assembleProvenance returns the timeline for one item: a synthesized
// capture event from the item record, all matching capture.log events,
// and all matching tags.log events. Sorted oldest-first since a
// timeline reads top-down chronologically.
func assembleProvenance(item *model.Item) ([]ProvenanceEvent, error) {
	out := []ProvenanceEvent{}

	// Synthesize the initial capture row from the item record. The
	// capture.log may or may not have a matching entry (older items
	// predate the log), but every item has a created_at so the
	// timeline always starts with a capture event.
	out = append(out, ProvenanceEvent{
		Timestamp: item.CreatedAt,
		Kind:      "capture",
		Summary:   summarizeCapture(string(item.Type), item.URL, item.SourcePath, extractDomain(item.URL)),
		URL:       item.URL,
		Domain:    extractDomain(item.URL),
	})

	logPath := rules.DefaultLogPath(config.Dir())
	if captureLog, err := rules.ReadEvents(logPath, 0); err == nil {
		for _, ev := range captureLog {
			if ev.ItemID != item.ID {
				continue
			}
			out = append(out, captureToProvenance(ev))
		}
	}

	tagPath := audit.DefaultTagsLogPath(config.Dir())
	if tagEvents, err := audit.ReadTagEvents(tagPath, 0); err == nil {
		for _, ev := range tagEvents {
			if ev.ItemID != item.ID {
				continue
			}
			out = append(out, tagToProvenance(ev))
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out, nil
}

func summarizeCapture(itemType, itemURL, sourcePath, domain string) string {
	var b strings.Builder
	switch itemType {
	case "link":
		b.WriteString("Captured URL")
	case "image":
		b.WriteString("Captured image")
	case "file":
		b.WriteString("Captured file")
	case "snippet":
		b.WriteString("Captured text snippet")
	case "email":
		b.WriteString("Captured email")
	default:
		b.WriteString("Captured item")
	}
	if domain != "" {
		b.WriteString(" from ")
		b.WriteString(domain)
	} else if sourcePath != "" {
		b.WriteString(" from ")
		b.WriteString(sourcePath)
	}
	return b.String()
}

func captureToProvenance(ev rules.Event) ProvenanceEvent {
	pe := ProvenanceEvent{
		Timestamp: ev.Timestamp,
		Source:    ev.Source,
		Effects:   ev.Effects,
	}
	switch ev.Type {
	case rules.EventCapture:
		pe.Kind = "capture"
		pe.Summary = "Captured via " + describeSurface(ev.Source)
	case rules.EventFire:
		pe.Kind = "rule"
		ruleName := strings.Join(ev.Rules, ", ")
		pe.Rule = ruleName
		if len(ev.Effects) > 0 {
			pe.Summary = fmt.Sprintf("Rule %s applied %s", ruleName, strings.Join(ev.Effects, " "))
		} else {
			pe.Summary = "Rule " + ruleName + " fired"
		}
	case rules.EventRetro:
		pe.Kind = "rule"
		ruleName := strings.Join(ev.Rules, ", ")
		pe.Rule = ruleName
		pe.Summary = fmt.Sprintf("Retroactive rule %s applied %s", ruleName, strings.Join(ev.Effects, " "))
	case rules.EventSkip:
		pe.Kind = "skip"
		pe.Rule = strings.Join(ev.Rules, ", ")
		pe.Summary = "Rule " + pe.Rule + " matched but was skipped"
	case rules.EventError:
		pe.Kind = "error"
		pe.Error = ev.Error
		pe.Summary = "Capture error: " + ev.Error
	}
	return pe
}

func tagToProvenance(ev audit.TagEvent) ProvenanceEvent {
	verb := "Added tag"
	if ev.Action == audit.ActionRemove {
		verb = "Removed tag"
	}
	summary := fmt.Sprintf("%s #%s", verb, ev.Tag)
	if ev.Source != "" {
		summary += " (" + ev.Source + ")"
	}
	return ProvenanceEvent{
		Timestamp: ev.Timestamp,
		Kind:      "tag",
		Summary:   summary,
		Tag:       ev.Tag,
		Action:    string(ev.Action),
		Source:    ev.Source,
	}
}

// describeSurface turns a raw capture.log source code into a
// user-facing label. Sources today: "chrome", "menubar", "selection",
// "sortie", "service", "drag-drop", "email", "cli", "fetch-url".
// Unknown values pass through as-is.
func describeSurface(s string) string {
	switch s {
	case "chrome":      return "Chrome extension"
	case "menubar":     return "menubar quick-stash"
	case "selection":   return "Selection Grabber hotkey"
	case "sortie":      return "Sortie folder watcher"
	case "service":     return "System Services menu"
	case "drag-drop":   return "drag-and-drop"
	case "email":       return "email ingest"
	case "cli":         return "stash CLI"
	case "fetch-url":   return "Fetch URL picker"
	case "":            return "an ingest surface"
	default:            return s
	}
}

func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	return strings.TrimPrefix(host, "www.")
}
