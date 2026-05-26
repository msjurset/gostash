package gemini

import (
	"strings"
)

// Parse is a Go port of stash-mac's AIResponseParser.parse. The
// default identify prompt asks the model for:
//
//	TITLE: <line>
//	NOTES: <line>
//	TRANSCRIPT: <multi-line block until EOF or next marker>
//
// Models occasionally wrap values in markdown (`**TITLE:**`),
// indent them, or use synonym headings (`Common Name:`,
// `Description:`). The parser is tolerant of all of those so a
// single prompt change doesn't ripple across every device.
//
// If no title marker matches, the entire response goes into Notes
// — same fallback behavior as the Swift parser.
func Parse(raw string) IdentifyResult {
	lines := strings.Split(raw, "\n")

	var (
		title string
		notes string
	)

	for _, line := range lines {
		if title == "" {
			if v, ok := extractValue(line, titleMarkers); ok {
				title = cleanInlineMarkers(v)
			}
		}
		if notes == "" {
			if v, ok := extractValue(line, notesMarkers); ok {
				notes = cleanInlineMarkers(v)
			}
		}
		if title != "" && notes != "" {
			break
		}
	}

	transcriptLines := extractMultilineValue(lines, transcriptMarkers)
	transcript := ""
	if transcriptLines != nil {
		joined := strings.TrimSpace(strings.Join(transcriptLines, "\n"))
		joined = cleanInlineMarkers(joined)
		if !(joined == "" || strings.EqualFold(joined, "NONE")) {
			transcript = joined
		}
	}
	transcriptLineSet := make(map[string]struct{}, len(transcriptLines))
	for _, l := range transcriptLines {
		transcriptLineSet[l] = struct{}{}
	}

	notesText := notes
	if notesText == "" {
		var keep []string
		for _, line := range lines {
			if _, isTitle := extractValue(line, titleMarkers); isTitle {
				continue
			}
			if _, isTr := extractValue(line, transcriptMarkers); isTr {
				continue
			}
			if _, ok := transcriptLineSet[line]; ok {
				continue
			}
			keep = append(keep, line)
		}
		joined := strings.TrimSpace(strings.Join(keep, "\n"))
		if joined == "" {
			notesText = raw
		} else {
			notesText = joined
		}
	}

	return IdentifyResult{
		Title:      strings.TrimSpace(title),
		Notes:      notesText,
		Transcript: transcript,
	}
}

var (
	titleMarkers      = []string{"TITLE", "Title", "Common Name", "Common name", "Name", "Subject"}
	notesMarkers      = []string{"NOTES", "Notes", "Description", "Details"}
	transcriptMarkers = []string{"TRANSCRIPT", "Transcript", "Text", "OCR"}
)

// extractValue tries each marker against the line; returns
// (value, true) when the line is `<leading ws/markdown>MARKER:<value>`.
// Case-insensitive marker match. Leading/trailing markdown
// emphasis around the value (`**...**`) is stripped — the model
// occasionally wraps individual values even when the prompt asks
// for plain text.
func extractValue(line string, markers []string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	stripped := strings.Trim(trimmed, "*_ ")
	for _, m := range markers {
		needle := m + ":"
		if len(stripped) < len(needle) {
			continue
		}
		if !strings.EqualFold(stripped[:len(needle)], needle) {
			continue
		}
		val := strings.TrimSpace(stripped[len(needle):])
		val = strings.Trim(val, "*_")
		val = strings.TrimSpace(val)
		return val, true
	}
	return "", false
}

// extractMultilineValue captures every line from the marker line
// (inclusive of any value on the marker line itself) up to the
// next known marker line or EOF. Used for TRANSCRIPT, which is a
// paragraph block rather than a single value.
//
// Returns nil when no marker line was found at all, so the caller
// can distinguish "no transcript section" from "transcript is
// empty / NONE".
func extractMultilineValue(lines []string, markers []string) []string {
	startIdx := -1
	for i, l := range lines {
		if _, ok := extractValue(l, markers); ok {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil
	}
	var out []string
	if first, ok := extractValue(lines[startIdx], markers); ok && first != "" {
		out = append(out, first)
	}
	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if _, ok := extractValue(line, titleMarkers); ok {
			break
		}
		if _, ok := extractValue(line, notesMarkers); ok {
			break
		}
		if _, ok := extractValue(line, transcriptMarkers); ok {
			break
		}
		out = append(out, line)
	}
	return out
}

// cleanInlineMarkers strips leading/trailing markdown emphasis off
// a value — same as the Swift String.cleanInlineMarkers extension.
// Only strips when the marker brackets the whole string; partial
// emphasis inside a value (e.g. "Boletus *edulis*") is preserved.
func cleanInlineMarkers(s string) string {
	s = strings.TrimSpace(s)
	for _, marker := range []string{"**", "__", "*", "_"} {
		if len(s) > len(marker)*2 &&
			strings.HasPrefix(s, marker) &&
			strings.HasSuffix(s, marker) {
			s = strings.TrimSpace(s[len(marker) : len(s)-len(marker)])
		}
	}
	return s
}
