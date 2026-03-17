package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func printItems(items []model.Item) {
	if flagJSON {
		printJSON(items)
		return
	}
	if len(items) == 0 {
		fmt.Println("No items found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tTITLE\tTAGS\tCREATED")
	for _, item := range items {
		tags := tagNames(item.Tags)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			shortID(item.ID),
			item.Type,
			truncate(item.Title, 50),
			tags,
			relTime(item.CreatedAt),
		)
	}
	w.Flush()
}

func printItem(item *model.Item) {
	if flagJSON {
		printJSON(item)
		return
	}

	fmt.Printf("ID:          %s\n", item.ID)
	fmt.Printf("Type:        %s\n", item.Type)
	fmt.Printf("Title:       %s\n", item.Title)
	if item.URL != "" {
		fmt.Printf("URL:         %s\n", item.URL)
	}
	if item.Notes != "" {
		fmt.Printf("Notes:       %s\n", item.Notes)
	}
	if item.MimeType != "" {
		fmt.Printf("MIME:        %s\n", item.MimeType)
	}
	if item.FileSize > 0 {
		fmt.Printf("Size:        %s\n", humanSize(item.FileSize))
	}
	if item.SourcePath != "" {
		fmt.Printf("Source:      %s\n", item.SourcePath)
	}
	if len(item.Tags) > 0 {
		fmt.Printf("Tags:        %s\n", tagNames(item.Tags))
	}
	if len(item.Collections) > 0 {
		names := make([]string, len(item.Collections))
		for i, c := range item.Collections {
			names[i] = c.Name
		}
		fmt.Printf("Collections: %s\n", strings.Join(names, ", "))
	}
	fmt.Printf("Created:     %s\n", item.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:     %s\n", item.UpdatedAt.Format(time.RFC3339))

	if item.ExtractedText != "" {
		fmt.Printf("\n--- Extracted Text ---\n%s\n", truncate(item.ExtractedText, 500))
	}
}

func printTags(tags []model.Tag) {
	if flagJSON {
		printJSON(tags)
		return
	}
	if len(tags) == 0 {
		fmt.Println("No tags found.")
		return
	}
	for _, t := range tags {
		fmt.Println(t.Name)
	}
}

func printCollections(cols []model.Collection) {
	if flagJSON {
		printJSON(cols)
		return
	}
	if len(cols) == 0 {
		fmt.Println("No collections found.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION")
	for _, c := range cols {
		fmt.Fprintf(w, "%s\t%s\n", c.Name, truncate(c.Description, 60))
	}
	w.Flush()
}

func shortID(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

func tagNames(tags []model.Tag) string {
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
