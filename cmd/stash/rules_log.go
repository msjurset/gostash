package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/rules"

	"github.com/spf13/cobra"
)

var rulesLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Show recent rule activity",
	Long: `Show events from $STASH_DIR/rules.log. Three event types:

  fire   Capture matched ≥1 rule and was saved
  skip   Capture matched a 'skip' rule and was dropped
  retro  ` + "`stash rules apply`" + ` produced a change on an existing item

Newest first. Use --tail (-f) to follow the log live.`,
	RunE: runRulesLog,
}

func init() {
	rulesLogCmd.Flags().String("type", "", "Filter by event type (fire, skip, retro)")
	rulesLogCmd.Flags().String("rule", "", "Filter to events involving the named rule")
	rulesLogCmd.Flags().IntP("limit", "l", 50, "Maximum events to show (0 = all)")
	rulesLogCmd.Flags().String("since", "", "Only show events newer than DURATION (e.g. 30m, 1h, 24h, 7d, 1w)")
	rulesLogCmd.Flags().BoolP("tail", "f", false, "Follow the log; stream new events as they arrive (Ctrl-C to stop)")
	rulesCmd.AddCommand(rulesLogCmd)
}

func runRulesLog(cmd *cobra.Command, args []string) error {
	typeFilter, _ := cmd.Flags().GetString("type")
	ruleFilter, _ := cmd.Flags().GetString("rule")
	limit, _ := cmd.Flags().GetInt("limit")
	sinceStr, _ := cmd.Flags().GetString("since")
	tail, _ := cmd.Flags().GetBool("tail")

	if typeFilter != "" && !validEventType(typeFilter) {
		return fmt.Errorf("invalid --type %q (want fire, skip, or retro)", typeFilter)
	}

	var since time.Time
	if sinceStr != "" {
		d, err := parseLogDuration(sinceStr)
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		since = time.Now().Add(-d)
	}

	path := rules.DefaultLogPath(config.Dir())

	// Read all events first; we filter and limit ourselves so the user
	// gets the most recent N matching events rather than the most recent
	// N events that may not match the filter.
	events, err := rules.ReadEvents(path, 0)
	if err != nil {
		return err
	}
	events = filterEvents(events, typeFilter, ruleFilter, since)
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	if err := emitEvents(events); err != nil {
		return err
	}

	if tail {
		return followLog(path, typeFilter, ruleFilter, since)
	}
	return nil
}

func validEventType(t string) bool {
	switch rules.EventType(t) {
	case rules.EventFire, rules.EventSkip, rules.EventRetro:
		return true
	}
	return false
}

// parseLogDuration extends Go's `time.ParseDuration` with day / week
// suffixes (`d`, `w`) since the activity log is naturally consulted on
// daily / weekly horizons. Falls back to `time.ParseDuration` for any
// other suffix (`30m`, `1h`, `2h30m`, etc.).
func parseLogDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid days value %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "w") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil {
			return 0, fmt.Errorf("invalid weeks value %q", s)
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func filterEvents(events []rules.Event, typeFilter, ruleFilter string, since time.Time) []rules.Event {
	if typeFilter == "" && ruleFilter == "" && since.IsZero() {
		return events
	}
	out := make([]rules.Event, 0, len(events))
	for _, ev := range events {
		if typeFilter != "" && string(ev.Type) != typeFilter {
			continue
		}
		if ruleFilter != "" {
			match := false
			for _, r := range ev.Rules {
				if r == ruleFilter {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// emitEvents writes events to stdout in the requested format. JSON mode
// emits one Event per line (JSONL), preserving the on-disk shape so
// callers can pipe through `jq` / etc. Default is a tabwriter table.
func emitEvents(events []rules.Event) error {
	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, ev := range events {
			if err := enc.Encode(ev); err != nil {
				return err
			}
		}
		return nil
	}
	if len(events) == 0 {
		fmt.Println("No rule activity yet.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "TIME\tTYPE\tRULE\tTITLE\tEFFECTS")
	for _, ev := range events {
		fmt.Fprintln(w, formatEventRow(ev))
	}
	return nil
}

func formatEventRow(ev rules.Event) string {
	ts := ev.Timestamp.Local().Format("2006-01-02 15:04")
	ruleStr := strings.Join(ev.Rules, ",")
	title := truncateForTable(ev.Title, 60)
	effects := strings.Join(ev.Effects, " ")
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", ts, ev.Type, ruleStr, title, effects)
}

func truncateForTable(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// followLog implements `--tail`: open the log, seek to end, and stream
// new lines as they're appended. Polls every 250ms — the log is small
// and writes are infrequent, so a goroutine + fsnotify would be overkill.
//
// Ctrl-C terminates naturally via SIGINT (Go's default handler exits).
// File rotation isn't supported in v1 because the engine doesn't rotate;
// if rotation is added later, this loop should detect a shrunk file and
// re-seek to 0.
func followLog(path, typeFilter, ruleFilter string, since time.Time) error {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var ev rules.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		// Apply filters in-stream so --tail respects --type / --rule /
		// --since just like the bulk read path.
		if typeFilter != "" && string(ev.Type) != typeFilter {
			continue
		}
		if ruleFilter != "" {
			match := false
			for _, r := range ev.Rules {
				if r == ruleFilter {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}

		if flagJSON {
			fmt.Println(line)
		} else {
			fmt.Println(formatEventRow(ev))
		}
	}
}
