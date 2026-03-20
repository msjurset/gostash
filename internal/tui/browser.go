package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/msjurset/gostash/internal/stash"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func openPath(path string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", path)
		case "linux":
			cmd = exec.Command("xdg-open", path)
		case "windows":
			cmd = exec.Command("cmd", "/c", "start", path)
		default:
			return openDoneMsg{err: fmt.Errorf("unsupported platform")}
		}
		return openDoneMsg{err: cmd.Start()}
	}
}

// browserEntry represents a single item in the file browser listing.
type browserEntry struct {
	name  string
	path  string
	isDir bool
	size  int64
}

// Messages for browser async operations.
type browserLoadedMsg struct {
	dir     string
	entries []browserEntry
	err     error
}

type stashDoneMsg struct {
	count int
	err   error
}

const (
	stashModeArchive    = 0
	stashModeIndividual = 1
)

// initBrowser sets up the file browser at the given directory.
func (m *Model) initBrowser() tea.Cmd {
	dir, err := os.Getwd()
	if err != nil {
		dir = os.Getenv("HOME")
	}
	m.browserDir = dir
	m.browserCursor = 0
	m.browserOffset = 0
	m.browserSelected = make(map[string]bool)
	m.activeView = viewFileBrowser
	return loadDirectory(dir)
}

func loadDirectory(dir string) tea.Cmd {
	return func() tea.Msg {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return browserLoadedMsg{dir: dir, err: err}
		}

		var result []browserEntry
		// Parent directory entry
		parent := filepath.Dir(dir)
		if parent != dir {
			result = append(result, browserEntry{name: "..", path: parent, isDir: true})
		}

		var dirs, files []browserEntry
		for _, e := range entries {
			// Skip hidden files
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			entry := browserEntry{
				name:  e.Name(),
				path:  filepath.Join(dir, e.Name()),
				isDir: e.IsDir(),
				size:  info.Size(),
			}
			if e.IsDir() {
				dirs = append(dirs, entry)
			} else {
				files = append(files, entry)
			}
		}

		sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
		sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
		result = append(result, dirs...)
		result = append(result, files...)

		return browserLoadedMsg{dir: dir, entries: result}
	}
}

// handleBrowserKey processes keys in the file browser view.
func (m *Model) handleBrowserKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Quit):
		m.activeView = viewList
		return m, nil

	case key.Matches(msg, keys.Help):
		m.activeView = viewHelp
		return m, nil

	case key.Matches(msg, keys.OpenExternal):
		if m.browserCursor < len(m.browserEntries) {
			entry := m.browserEntries[m.browserCursor]
			if entry.name != ".." {
				return m, openPath(entry.path)
			}
		}
		return m, nil

	case key.Matches(msg, keys.Up):
		if m.browserCursor > 0 {
			m.browserCursor--
			if m.browserCursor < m.browserOffset {
				m.browserOffset = m.browserCursor
			}
		}

	case key.Matches(msg, keys.Down):
		if m.browserCursor < len(m.browserEntries)-1 {
			m.browserCursor++
			if m.browserCursor >= m.browserOffset+m.listRows {
				m.browserOffset = m.browserCursor - m.listRows + 1
			}
		}

	case msg.String() == "right" || msg.String() == "l":
		if m.browserCursor < len(m.browserEntries) {
			entry := m.browserEntries[m.browserCursor]
			if entry.isDir {
				m.browserCursor = 0
				m.browserOffset = 0
				m.browserSelected = make(map[string]bool)
				return m, loadDirectory(entry.path)
			}
		}

	case msg.String() == "left" || msg.String() == "h":
		parent := filepath.Dir(m.browserDir)
		if parent != m.browserDir {
			m.browserCursor = 0
			m.browserOffset = 0
			m.browserSelected = make(map[string]bool)
			return m, loadDirectory(parent)
		}

	case key.Matches(msg, keys.Toggle):
		if m.browserCursor < len(m.browserEntries) {
			entry := m.browserEntries[m.browserCursor]
			if entry.name != ".." {
				if m.browserSelected[entry.path] {
					delete(m.browserSelected, entry.path)
				} else {
					m.browserSelected[entry.path] = true
				}
			}
		}

	case key.Matches(msg, keys.Enter):
		if m.browserCursor >= len(m.browserEntries) {
			return m, nil
		}
		entry := m.browserEntries[m.browserCursor]

		// If there are selections and enter is pressed, proceed to stash
		if len(m.browserSelected) > 0 {
			return m, m.startStashFlow()
		}

		// No selections: navigate into directory
		if entry.isDir {
			m.browserCursor = 0
			m.browserOffset = 0
			return m, loadDirectory(entry.path)
		}

	case key.Matches(msg, keys.SelectAll):
		// Toggle all files in current listing
		allSelected := true
		for _, e := range m.browserEntries {
			if e.name != ".." && !m.browserSelected[e.path] {
				allSelected = false
				break
			}
		}
		for _, e := range m.browserEntries {
			if e.name == ".." {
				continue
			}
			if allSelected {
				delete(m.browserSelected, e.path)
			} else {
				m.browserSelected[e.path] = true
			}
		}
	}

	return m, nil
}

// startStashFlow transitions from the browser to the appropriate stash view.
func (m *Model) startStashFlow() tea.Cmd {
	selected := m.selectedPaths()
	if len(selected) == 0 {
		return nil
	}

	if len(selected) == 1 {
		// Single item — go straight to details
		m.stashMode = stashModeIndividual
		m.detailQueue = selected
		m.detailCurrent = 0
		m.initStashDetails(selected[0])
		m.activeView = viewStashDetails
		return textinput.Blink
	}

	// Multiple items — ask archive or individual
	m.stashMode = stashModeArchive
	m.activeView = viewStashConfirm
	return nil
}

func (m *Model) selectedPaths() []string {
	var paths []string
	for path := range m.browserSelected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (m *Model) initStashDetails(path string) {
	m.detailTitle.SetValue(filepath.Base(path))
	m.detailTags.SetValue("")
	m.detailNote.SetValue("")
	m.detailDelete = false
	m.detailFocus = 0
	m.detailTitle.Focus()
	m.detailTags.Blur()
	m.detailNote.Blur()
}

// handleStashConfirmKey processes keys in the archive-or-individual prompt.
func (m *Model) handleStashConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Escape):
		m.activeView = viewFileBrowser
		return m, nil

	case key.Matches(msg, keys.Up), key.Matches(msg, keys.Down):
		if m.stashMode == stashModeArchive {
			m.stashMode = stashModeIndividual
		} else {
			m.stashMode = stashModeArchive
		}

	case key.Matches(msg, keys.Enter):
		selected := m.selectedPaths()
		if m.stashMode == stashModeArchive {
			m.detailQueue = nil
			m.detailCurrent = 0
			title := fmt.Sprintf("Archive (%d items)", len(selected))
			m.detailTitle.SetValue(title)
			m.detailTags.SetValue("")
			m.detailNote.SetValue("")
			m.detailDelete = false
			m.detailFocus = 0
			m.detailTitle.Focus()
			m.detailTags.Blur()
			m.detailNote.Blur()
		} else {
			m.detailQueue = selected
			m.detailCurrent = 0
			m.initStashDetails(selected[0])
		}
		m.activeView = viewStashDetails
		return m, textinput.Blink
	}

	return m, nil
}

// handleStashDetailsKey processes keys in the stash details form.
func (m *Model) handleStashDetailsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Escape):
		if len(m.selectedPaths()) > 1 {
			m.activeView = viewStashConfirm
		} else {
			m.activeView = viewFileBrowser
		}
		return m, nil

	case msg.String() == "tab":
		m.detailFocus = (m.detailFocus + 1) % 4
		m.syncDetailFocus()
		return m, nil

	case msg.String() == "shift+tab":
		m.detailFocus = (m.detailFocus + 3) % 4
		m.syncDetailFocus()
		return m, nil

	case key.Matches(msg, keys.Toggle):
		if m.detailFocus == 3 {
			m.detailDelete = !m.detailDelete
			return m, nil
		}
		// Space in text fields should be passed through
		return m, m.updateDetailInput(msg)

	case key.Matches(msg, keys.Enter):
		return m, m.submitStash()

	default:
		return m, m.updateDetailInput(msg)
	}
}

func (m *Model) syncDetailFocus() {
	m.detailTitle.Blur()
	m.detailTags.Blur()
	m.detailNote.Blur()
	switch m.detailFocus {
	case 0:
		m.detailTitle.Focus()
	case 1:
		m.detailTags.Focus()
	case 2:
		m.detailNote.Focus()
	}
}

func (m *Model) updateDetailInput(msg tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	switch m.detailFocus {
	case 0:
		m.detailTitle, cmd = m.detailTitle.Update(msg)
	case 1:
		m.detailTags, cmd = m.detailTags.Update(msg)
	case 2:
		m.detailNote, cmd = m.detailNote.Update(msg)
	}
	return cmd
}

func (m *Model) submitStash() tea.Cmd {
	title := m.detailTitle.Value()
	tagsStr := m.detailTags.Value()
	note := m.detailNote.Value()
	deleteSrc := m.detailDelete

	var tags []string
	for _, t := range strings.Split(tagsStr, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}

	p := stash.Params{
		Title:        title,
		Tags:         tags,
		Note:         note,
		DeleteSource: deleteSrc,
	}

	selected := m.selectedPaths()
	mode := m.stashMode

	// For individual mode, get current file
	if mode == stashModeIndividual && m.detailCurrent < len(m.detailQueue) {
		path := m.detailQueue[m.detailCurrent]
		s := m.store
		fs := m.files
		return func() tea.Msg {
			fi, err := os.Stat(path)
			if err != nil {
				return stashDoneMsg{err: err}
			}
			if fi.IsDir() {
				_, err = stash.Directory(context.Background(), s, fs, path, p)
			} else {
				_, err = stash.File(context.Background(), s, fs, path, p)
			}
			if err != nil {
				return stashDoneMsg{err: err}
			}
			return stashDoneMsg{count: 1}
		}
	}

	// Archive mode — all selected paths into one tar.gz
	s := m.store
	fs := m.files
	return func() tea.Msg {
		_, err := stash.Archive(context.Background(), s, fs, selected, p)
		if err != nil {
			return stashDoneMsg{err: err}
		}
		return stashDoneMsg{count: len(selected)}
	}
}

// handleStashDone processes the result of a stash operation.
func (m *Model) handleStashDone(msg stashDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.activeView = viewFileBrowser
		return m, nil
	}

	// Individual mode — advance to next file or finish
	if m.stashMode == stashModeIndividual && m.detailQueue != nil {
		m.detailCurrent++
		if m.detailCurrent < len(m.detailQueue) {
			m.initStashDetails(m.detailQueue[m.detailCurrent])
			m.activeView = viewStashDetails
			return m, textinput.Blink
		}
	}

	// Done — return to browser, refresh both browser and item list
	m.browserSelected = make(map[string]bool)
	m.activeView = viewFileBrowser
	m.err = nil
	return m, tea.Batch(loadDirectory(m.browserDir), m.fetchItems())
}

// --- View rendering ---

func (m Model) viewFileBrowser() string {
	header := headerStyle.Width(m.width).Render(
		fmt.Sprintf("  File Browser  (%s)", m.browserDir))

	var b strings.Builder
	b.WriteString(header + "\n")

	if m.err != nil {
		b.WriteString(errStyle.Render(fmt.Sprintf("  Error: %v", m.err)) + "\n")
	}

	if len(m.browserEntries) == 0 {
		b.WriteString(dimStyle.Render("  Empty directory.") + "\n")
	} else {
		end := m.browserOffset + m.listRows
		if end > len(m.browserEntries) {
			end = len(m.browserEntries)
		}
		for i := m.browserOffset; i < end; i++ {
			entry := m.browserEntries[i]
			selected := i == m.browserCursor

			isMarked := m.browserSelected[entry.path]

			if selected {
				// Build plain text line — selectedStyle handles all coloring
				check := "   "
				if entry.name != ".." {
					if isMarked {
						check = " \u2713 "
					} else {
						check = " \u00b7 "
					}
				}
				name := entry.name
				if entry.isDir && entry.name != ".." {
					name += "/"
				}
				sizeStr := ""
				if !entry.isDir {
					sizeStr = humanSize(entry.size)
				}
				line := fmt.Sprintf(" %s %s", check, name)
				if sizeStr != "" {
					padding := m.width - len(line) - len(sizeStr) - 4
					if padding < 1 {
						padding = 1
					}
					line += strings.Repeat(" ", padding) + sizeStr
				}
				b.WriteString(renderSelected(line, m.width))
			} else {
				// Styled line for non-cursor rows
				check := "   "
				if entry.name != ".." {
					if isMarked {
						check = selectedCheckStyle.Render(" \u2713 ")
					} else {
						check = dimStyle.Render(" \u00b7 ")
					}
				}
				name := entry.name
				if entry.isDir && entry.name != ".." {
					if isMarked {
						name = selectedCheckStyle.Render(name + "/")
					} else {
						name = dirEntryStyle.Render(name + "/")
					}
				} else if entry.name == ".." {
					name = dimStyle.Render("..")
				} else if isMarked {
					name = selectedCheckStyle.Render(name)
				}
				sizeStr := ""
				if !entry.isDir {
					sizeStr = dimStyle.Render(humanSize(entry.size))
				}
				line := fmt.Sprintf(" %s %s", check, name)
				if sizeStr != "" {
					padding := m.width - len(stripAnsi(line)) - len(stripAnsi(sizeStr)) - 2
					if padding < 1 {
						padding = 1
					}
					line += strings.Repeat(" ", padding) + sizeStr
				}
				b.WriteString(" " + line)
			}
			b.WriteString("\n")
		}
	}

	// Pad to fill
	used := strings.Count(b.String(), "\n")
	for i := used; i < m.height-1; i++ {
		b.WriteString("\n")
	}

	// Status bar
	selCount := len(m.browserSelected)
	left := fmt.Sprintf(" %d selected", selCount)
	right := " space:select  a:all  o:open  enter:stash  ?:help  q:back "
	gap := m.width - len(left) - len(right)
	if gap < 0 {
		gap = 0
	}
	b.WriteString(statusStyle.Width(m.width).Render(left + strings.Repeat(" ", gap) + right))

	return b.String()
}

func (m Model) viewStashConfirm() string {
	header := headerStyle.Width(m.width).Render("  Stash Selected Items")

	selCount := len(m.browserSelected)
	var b strings.Builder
	b.WriteString(header + "\n\n")
	b.WriteString(fmt.Sprintf("  %d items selected. How would you like to stash them?\n\n", selCount))

	options := []string{
		"Archive everything as a single tar.gz",
		"Stash each item individually",
	}
	for i, opt := range options {
		line := "   " + opt
		if i == m.stashMode {
			b.WriteString(renderSelected(line, m.width))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n" + dimStyle.Render("  j/k to choose, enter to confirm, esc to cancel") + "\n")

	return b.String()
}

func (m Model) viewStashDetails() string {
	var title string
	if m.stashMode == stashModeArchive {
		title = "  Stash Archive"
	} else if m.detailQueue != nil && m.detailCurrent < len(m.detailQueue) {
		if len(m.detailQueue) > 1 {
			title = fmt.Sprintf("  Stash File (%d of %d)", m.detailCurrent+1, len(m.detailQueue))
		} else {
			title = "  Stash File"
		}
	} else {
		title = "  Stash"
	}

	header := headerStyle.Width(m.width).Render(title)

	var b strings.Builder
	b.WriteString(header + "\n\n")

	// Show current file/path
	if m.stashMode == stashModeIndividual && m.detailCurrent < len(m.detailQueue) {
		b.WriteString(dimStyle.Render("  Path: "+m.detailQueue[m.detailCurrent]) + "\n\n")
	} else if m.stashMode == stashModeArchive {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %d items will be archived together", len(m.browserSelected))) + "\n\n")
	}

	fields := []struct {
		label string
		view  string
	}{
		{"Title", m.detailTitle.View()},
		{"Tags", m.detailTags.View()},
		{"Note", m.detailNote.View()},
	}

	for i, f := range fields {
		label := f.label
		if i == m.detailFocus {
			label = detailLabel.Render(label)
		} else {
			label = dimStyle.Render(label)
		}
		b.WriteString(fmt.Sprintf("  %s\n  %s\n\n", label, f.view))
	}

	// Delete source toggle
	check := "[ ]"
	if m.detailDelete {
		check = "[x]"
	}
	label := "Delete source after stash"
	if m.detailFocus == 3 {
		b.WriteString(fmt.Sprintf("  %s %s\n", detailLabel.Render(check), detailLabel.Render(label)))
	} else {
		b.WriteString(fmt.Sprintf("  %s %s\n", dimStyle.Render(check), dimStyle.Render(label)))
	}

	b.WriteString("\n" + dimStyle.Render("  tab:next field  space:toggle delete  enter:stash  esc:cancel") + "\n")

	return b.String()
}

// renderSelected applies the selected style with full-width background.
func renderSelected(line string, width int) string {
	return selectedStyle.Width(width - 2).Render(line)
}

// stripAnsi removes ANSI escape sequences for length calculation.
func stripAnsi(s string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
