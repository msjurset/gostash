package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var findCmd = &cobra.Command{
	Use:   "find",
	Short: "Interactive fuzzy finder for stashed items (requires fzf)",
	Long: `Pipe stashed items into fzf for interactive fuzzy selection. Each line
combines a one-letter type indicator, title, tags (rendered as #name),
and domain so any of those fields participates in the fuzzy match. The
preview pane shows the full item details via 'stash show'.

Inline filter tokens you can type in the search box:

  #tagname        Match items carrying that tag
  type:url        URL items only (alias: type:link)
  type:snippet    Snippets only (or type:file, type:image, type:email)
  example.com     Any token matches against the domain too
  !youtube        Exclude lines containing "youtube"
  '"exact"        Exact (non-fuzzy) match (single-quote prefix)

Default action on Enter is to open the item (URL in the default
browser, file/image/email in their default app, snippet in $PAGER).
Other actions: --action copy-url, copy-id, edit, delete, print-id,
print-json.

Filter flags (--type, --tag, --exclude-tag, --untagged, --collection,
--recent, --regex, --include-archived, --archived, --before, --after,
--limit) compose with fzf's fuzzy match: pre-filter server-side, then
fuzzy-narrow client-side.

Keybinds inside the picker:
  Enter        run the selected action and exit
  Ctrl-Y       copy the item's URL (or extracted text) to the clipboard
  Alt-I        copy the item's full ID to the clipboard
  Ctrl-T       open a tag picker; pick one to filter the list to it
  Alt-R        reset all pre-filters (back to the full library)
  ?            toggle the preview pane
  Esc / Ctrl-C cancel`,
	// Errors from runFind / runFindAction are runtime failures, not
	// "you typed the command wrong" — silence cobra's default usage
	// dump so the user gets just the error message.
	SilenceUsage: true,
	RunE:         runFind,
}

func init() {
	addSearchFilterFlags(findCmd)
	findCmd.Flags().String("action", "open", "Action on Enter: open, copy-url, copy-id, edit, delete, print-id, print-json")
	rootCmd.AddCommand(findCmd)
}

func runFind(cmd *cobra.Command, args []string) error {
	if _, err := exec.LookPath("fzf"); err != nil {
		return fmt.Errorf("fzf not found on PATH. Install with `brew install fzf` or your distro's package manager.")
	}

	filter, err := buildFilter(cmd, "")
	if err != nil {
		return err
	}
	// Override the default 50-row limit — `find` is interactive
	// browsing, not a paginated CLI list, so we want the full
	// library available to fuzzy match against. Cap at 10k to
	// keep fzf snappy on enormous libraries.
	if filter.Limit <= 50 {
		filter.Limit = 10000
	}

	selfPath, err := os.Executable()
	if err != nil || selfPath == "" {
		selfPath = "stash"
	}

	// State file holds the current filter as quoted shell args. The
	// initial command-line flags seed the file; mid-flight keybinds
	// (Ctrl-T tag picker, Alt-R reset) overwrite it before triggering
	// fzf's reload action. Cleaned up on exit.
	stateFile, err := os.CreateTemp("", "stash-find-state-*")
	if err != nil {
		return fmt.Errorf("create state file: %w", err)
	}
	statePath := stateFile.Name()
	stateFile.Close()
	defer os.Remove(statePath)

	pickFile := statePath + ".pick"
	defer os.Remove(pickFile)

	if err := os.WriteFile(statePath, []byte(filterToArgsLine(filter)), 0o600); err != nil {
		return fmt.Errorf("seed state file: %w", err)
	}

	// `bash -c "$STASH_BIN find-list $(cat $STASH_STATE)"` is the
	// common reload incantation — fzf evaluates the inner $(...)
	// inside execute(...)/reload(...) by way of the shell.
	reloadCmd := `bash -c "$STASH_BIN find-list $(cat $STASH_STATE)"`

	// Tag picker: nest fzf to choose a tag from `stash tag list`,
	// stash the choice in the pick file, write the new filter to
	// the state file, then reload main fzf. If the user cancels
	// the inner fzf (no pick written), state stays unchanged and
	// the reload no-ops on stale data.
	tagPick := `bash -c '$STASH_BIN tag list 2>/dev/null | fzf --no-multi --prompt="tag> " --header="Pick a tag (Esc to cancel)" > "$STASH_PICK" 2>/dev/null && [ -s "$STASH_PICK" ] && printf -- "--tag %s" "$(cat $STASH_PICK)" > "$STASH_STATE"'`

	// Reset: blank state file -> reload -> all items.
	resetCmd := `bash -c '> "$STASH_STATE"'`

	fzfArgs := []string{
		"--ansi",
		"--no-multi",
		"--no-hscroll",
		"--delimiter=\t",
		"--with-nth=2..",
		"--preview", `$STASH_BIN show {1} 2>/dev/null`,
		"--preview-window=right:50%:wrap",
		"--header", buildFindHeader(filter, -1),
		"--prompt=stash> ",
		// Initial load via reload-on-start so the same code path
		// drives both first paint and mid-flight refreshes.
		"--bind", `start:reload(` + reloadCmd + `)`,
		"--bind", `ctrl-y:execute-silent($STASH_BIN copy {1} --field url)`,
		"--bind", `alt-i:execute-silent($STASH_BIN copy {1} --field id)`,
		"--bind", `ctrl-t:execute(` + tagPick + `)+reload(` + reloadCmd + `)`,
		"--bind", `alt-r:execute(` + resetCmd + `)+reload(` + reloadCmd + `)`,
		"--bind", "?:toggle-preview",
	}

	fzfCmd := exec.Command("fzf", fzfArgs...)
	fzfCmd.Stderr = os.Stderr
	fzfCmd.Env = append(os.Environ(),
		"STASH_BIN="+selfPath,
		"STASH_STATE="+statePath,
		"STASH_PICK="+pickFile,
	)
	var out bytes.Buffer
	fzfCmd.Stdout = &out

	if err := fzfCmd.Run(); err != nil {
		// Exit 130 = user pressed Esc / Ctrl-C in fzf. Not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return nil
		}
		return fmt.Errorf("fzf: %w", err)
	}

	line := strings.TrimSpace(out.String())
	if line == "" {
		return nil
	}
	parts := strings.SplitN(line, "\t", 2)
	if len(parts) < 1 || parts[0] == "" {
		return nil
	}
	selectedID := parts[0]

	action, _ := cmd.Flags().GetString("action")
	return runFindAction(selectedID, action)
}

// formatFindLine is the per-item input row given to fzf. The line is
// two tab-separated columns:
//
//	col 1 (hidden via --with-nth=2..): full ULID — used by the
//	  preview command and clipboard keybinds via fzf's {1} expansion.
//	col 2 (visible, one big string): the human-readable row,
//	  internally space-separated. Tabs inside the visible portion
//	  align to the terminal's tab-stop and produce a giant gap, so
//	  we use spaces here.
//
// Visible layout:
//
//	<L>  <title-padded-to-60>   <#tags>   <domain>   <type:foo>
//
// The trailing `type:foo` tokens are deliberate: fzf hides any column
// excluded by --with-nth from matching too, so the only way to make
// `type:url rick` work as an inline filter is to include the tokens
// in the visible column. They sit at the end so long titles push them
// off the right edge — visually noise-free for the common case, still
// searchable in either direction. Tags + domain stay searchable too.
func formatFindLine(item *model.Item) string {
	letter := typeLetterForFind(item.Type)
	title := item.Title
	if title == "" {
		title = "(untitled)"
	}
	title = padOrTruncate(title, 60)

	tags := ""
	if len(item.Tags) > 0 {
		ts := make([]string, len(item.Tags))
		for i, t := range item.Tags {
			ts[i] = "#" + t.Name
		}
		tags = strings.Join(ts, " ")
	}
	domain := urlHostForFind(item.URL)
	typeTokens := typeTokensForFind(item.Type)

	visible := letter + "  " + title
	if tags != "" {
		visible += "   " + tags
	}
	if domain != "" {
		visible += "   " + domain
	}
	if typeTokens != "" {
		visible += "   " + typeTokens
	}
	return item.ID + "\t" + visible
}

// typeTokensForFind builds the hidden search-only tokens for a given
// item type. URL items get both `type:url` (the user-facing name) and
// `type:link` (the internal model name) so either spelling works in
// the search field.
func typeTokensForFind(t model.ItemType) string {
	switch t {
	case model.TypeURL:
		return "type:url type:link"
	case model.TypeSnippet:
		return "type:snippet"
	case model.TypeFile:
		return "type:file"
	case model.TypeImage:
		return "type:image"
	case model.TypeEmail:
		return "type:email"
	}
	return ""
}

func typeLetterForFind(t model.ItemType) string {
	switch t {
	case model.TypeURL:
		return "L"
	case model.TypeSnippet:
		return "S"
	case model.TypeFile:
		return "F"
	case model.TypeImage:
		return "I"
	case model.TypeEmail:
		return "E"
	}
	return "?"
}

// padOrTruncate normalizes a string to exactly `width` runes —
// truncated with an ellipsis if longer, space-padded if shorter.
// Operates on runes (not bytes) so multi-byte characters don't
// break the alignment.
func padOrTruncate(s string, width int) string {
	r := []rune(s)
	if len(r) > width {
		return string(r[:width-1]) + "…"
	}
	if len(r) < width {
		return s + strings.Repeat(" ", width-len(r))
	}
	return s
}

func urlHostForFind(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Cheap host extract — avoid pulling net/url in a hot path
	// when the URL is already broken or empty.
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// buildFindHeader produces the static header line shown above the fzf
// list. `count` is supplied as -1 when called before the initial
// reload (we don't know yet how many items will land); it then renders
// without an item count. The keybind hints stay constant.
func buildFindHeader(filter model.ItemFilter, count int) string {
	var parts []string
	if filter.Type != "" {
		parts = append(parts, "type="+string(filter.Type))
	}
	if len(filter.Tags) > 0 {
		parts = append(parts, "tag="+strings.Join(filter.Tags, ","))
	}
	if len(filter.ExcludeTags) > 0 {
		parts = append(parts, "-tag="+strings.Join(filter.ExcludeTags, ","))
	}
	if filter.Untagged {
		parts = append(parts, "untagged")
	}
	if filter.Collection != "" {
		parts = append(parts, "col="+filter.Collection)
	}
	if filter.Recent != "" {
		parts = append(parts, "recent="+filter.Recent)
	}
	if filter.Regex != "" {
		parts = append(parts, "re="+filter.Regex)
	}
	if filter.OnlyArchived {
		parts = append(parts, "archived")
	}
	const hints = "?: preview · ⌃Y: copy URL · ⌥I: copy ID · ⌃T: pick tag · ⌥R: reset"
	prefix := ""
	if count >= 0 {
		prefix = fmt.Sprintf("%d items · ", count)
	}
	if len(parts) == 0 {
		return prefix + hints
	}
	return prefix + strings.Join(parts, " ") + " · " + hints
}

// filterToArgsLine renders an ItemFilter into a shell-quoted args
// string suitable to be `cat`'d into a `stash find-list ...` command.
// Single-quoted so paths/regexes with spaces survive intact.
func filterToArgsLine(f model.ItemFilter) string {
	var args []string
	add := func(parts ...string) { args = append(args, parts...) }
	if f.Type != "" {
		add("--type", string(f.Type))
	}
	for _, tag := range f.Tags {
		add("--tag", tag)
	}
	for _, tag := range f.ExcludeTags {
		add("--exclude-tag", tag)
	}
	if f.Untagged {
		add("--untagged")
	}
	if f.Collection != "" {
		add("--collection", f.Collection)
	}
	if f.Recent != "" {
		add("--recent", f.Recent)
	}
	if f.Regex != "" {
		add("--regex", f.Regex)
	}
	if f.OnlyArchived {
		add("--archived")
	} else if f.IncludeArchived {
		add("--include-archived")
	}
	if f.Limit > 0 {
		add("--limit", fmt.Sprintf("%d", f.Limit))
	}
	return shellQuoteArgs(args)
}

func shellQuoteArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}


// runFindAction dispatches the post-selection action against the
// chosen item. Reuses subcommand entry points so behavior matches
// what the user gets running them directly (delete's confirmation,
// edit's interactive flow, copy's clipboard fallbacks, etc.).
func runFindAction(itemID, action string) error {
	switch action {
	case "", "open":
		return runOpen(openCmd, []string{itemID})
	case "edit":
		return execSelf("edit", itemID)
	case "delete":
		return execSelf("delete", itemID)
	case "copy-url":
		return execSelf("copy", itemID, "--field", "url")
	case "copy-id":
		return execSelf("copy", itemID, "--field", "id")
	case "print-id":
		fmt.Println(itemID)
		return nil
	case "print-json":
		return execSelf("show", itemID, "--json")
	}
	return fmt.Errorf("unknown action: %s", action)
}

// execSelf invokes another stash subcommand via the current binary,
// inheriting stdio so prompts/output flow through.
func execSelf(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		self = "stash"
	}
	c := exec.Command(self, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
