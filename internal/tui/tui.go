package tui

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// view represents which panel is active.
type view int

const (
	viewList view = iota
	viewDetail
	viewLinkSearch
	viewLinkLabel
	viewUnlinkSelect
	viewDeleteConfirm
	viewHelp
)

// Model is the top-level bubbletea model.
type Model struct {
	store store.Store
	files *filestore.FileStore
	width int
	height int

	// search
	search    textinput.Model
	searching bool

	// list
	items    []model.Item
	cursor   int
	offset   int
	listRows int

	// detail
	activeView view
	detail     viewport.Model

	// filter state
	filter model.ItemFilter

	// link mode state
	linkSearch    textinput.Model
	linkLabel     textinput.Model
	linkResults   []model.Item
	linkCursor    int
	linkTargetID  string
	unlinkCursor  int

	// delete confirmation
	deleteReturnView view

	err error
}

type itemsMsg []model.Item
type errMsg error
type openDoneMsg struct{ err error }
type linkSearchMsg []model.Item
type linkDoneMsg struct{ err error }
type unlinkDoneMsg struct{ err error }
type refreshedItemMsg struct{ item *model.Item }
type deleteDoneMsg struct{ err error }

// New creates a new TUI model.
func New(s store.Store, fs *filestore.FileStore) Model {
	ti := textinput.New()
	ti.Placeholder = "Search..."
	ti.CharLimit = 256

	ls := textinput.New()
	ls.Placeholder = "Search for item to link..."
	ls.CharLimit = 256

	ll := textinput.New()
	ll.Placeholder = "Label (Enter to skip)..."
	ll.CharLimit = 256

	return Model{
		store:      s,
		files:      fs,
		search:     ti,
		linkSearch: ls,
		linkLabel:  ll,
		filter:     model.ItemFilter{Limit: 100},
	}
}

func (m Model) Init() tea.Cmd {
	return m.fetchItems()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.listRows = m.height - 4 // header + search + status + border
		if m.listRows < 1 {
			m.listRows = 1
		}
		m.detail = viewport.New(m.width, m.height-2)
		return m, nil

	case itemsMsg:
		m.items = msg
		m.cursor = 0
		m.offset = 0
		m.err = nil
		return m, nil

	case errMsg:
		m.err = msg
		return m, nil

	case openDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, nil

	case linkSearchMsg:
		m.linkResults = msg
		return m, nil

	case linkDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.activeView = viewDetail
			// Refresh the current item to show new link
			if m.cursor < len(m.items) {
				return m, m.refreshCurrentItem()
			}
		}
		return m, nil

	case unlinkDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.activeView = viewDetail
			if m.cursor < len(m.items) {
				return m, m.refreshCurrentItem()
			}
		}
		return m, nil

	case refreshedItemMsg:
		if msg.item != nil && m.cursor < len(m.items) {
			m.items[m.cursor] = *msg.item
			m.detail.SetContent(m.renderDetail(msg.item, m.width))
		}
		return m, nil

	case deleteDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.activeView = m.deleteReturnView
		} else {
			m.activeView = viewList
			return m, m.fetchItems()
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.searching {
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}

	if m.activeView == viewLinkSearch {
		var cmd tea.Cmd
		m.linkSearch, cmd = m.linkSearch.Update(msg)
		return m, cmd
	}

	if m.activeView == viewLinkLabel {
		var cmd tea.Cmd
		m.linkLabel, cmd = m.linkLabel.Update(msg)
		return m, cmd
	}

	if m.activeView == viewDetail {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits
	if key.Matches(msg, keys.ForceQuit) {
		return m, tea.Quit
	}

	// Search mode
	if m.searching {
		switch {
		case key.Matches(msg, keys.Enter):
			m.searching = false
			m.search.Blur()
			m.parseSearch(m.search.Value())
			return m, m.fetchItems()
		case key.Matches(msg, keys.Escape):
			m.searching = false
			m.search.Blur()
			return m, nil
		default:
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			return m, cmd
		}
	}

	// Link search mode
	if m.activeView == viewLinkSearch {
		switch msg.String() {
		case "esc":
			m.activeView = viewDetail
			m.linkSearch.Blur()
			m.linkResults = nil
			m.linkCursor = 0
			return m, nil
		case "enter":
			if len(m.linkResults) > 0 && m.linkCursor < len(m.linkResults) {
				m.linkTargetID = m.linkResults[m.linkCursor].ID
				m.activeView = viewLinkLabel
				m.linkSearch.Blur()
				m.linkLabel.SetValue("")
				m.linkLabel.Focus()
				m.linkCursor = 0
				return m, textinput.Blink
			}
			return m, nil
		case "tab", "down":
			if len(m.linkResults) > 0 {
				m.linkSearch.Blur()
				m.linkCursor = (m.linkCursor + 1) % len(m.linkResults)
			}
			return m, nil
		case "shift+tab", "up":
			if len(m.linkResults) > 0 {
				m.linkSearch.Blur()
				m.linkCursor = (m.linkCursor - 1 + len(m.linkResults)) % len(m.linkResults)
			}
			return m, nil
		default:
			if !m.linkSearch.Focused() {
				m.linkSearch.Focus()
			}
			var cmd tea.Cmd
			m.linkSearch, cmd = m.linkSearch.Update(msg)
			// Trigger search on every keystroke
			query := m.linkSearch.Value()
			if query != "" {
				m.linkCursor = 0
				return m, tea.Batch(cmd, m.searchForLink(query))
			}
			m.linkResults = nil
			m.linkCursor = 0
			return m, cmd
		}
	}

	// Link label mode
	if m.activeView == viewLinkLabel {
		switch {
		case key.Matches(msg, keys.Escape):
			m.activeView = viewDetail
			m.linkLabel.Blur()
			return m, nil
		case key.Matches(msg, keys.Enter):
			label := m.linkLabel.Value()
			m.linkLabel.Blur()
			return m, m.createLink(m.linkTargetID, label)
		default:
			var cmd tea.Cmd
			m.linkLabel, cmd = m.linkLabel.Update(msg)
			return m, cmd
		}
	}

	// Help view — any key dismisses
	if m.activeView == viewHelp {
		m.activeView = viewList
		return m, nil
	}

	// Delete confirmation mode
	if m.activeView == viewDeleteConfirm {
		switch msg.String() {
		case "y", "Y":
			if m.cursor < len(m.items) {
				return m, m.deleteCurrentItem()
			}
			m.activeView = m.deleteReturnView
			return m, nil
		default:
			m.activeView = m.deleteReturnView
			return m, nil
		}
	}

	// Unlink select mode
	if m.activeView == viewUnlinkSelect {
		switch msg.String() {
		case "esc", "q":
			m.activeView = viewDetail
			return m, nil
		case "up", "k":
			if m.unlinkCursor > 0 {
				m.unlinkCursor--
			}
			return m, nil
		case "down", "j":
			if m.cursor < len(m.items) {
				item := &m.items[m.cursor]
				if m.unlinkCursor < len(item.Links)-1 {
					m.unlinkCursor++
				}
			}
			return m, nil
		case "enter":
			if m.cursor < len(m.items) {
				item := &m.items[m.cursor]
				if m.unlinkCursor < len(item.Links) {
					targetID := item.Links[m.unlinkCursor].ItemID
					return m, m.removeLink(targetID)
				}
			}
			return m, nil
		}
		return m, nil
	}

	// Detail view
	if m.activeView == viewDetail {
		switch {
		case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Quit):
			m.activeView = viewList
			return m, nil
		case key.Matches(msg, keys.Help):
			m.activeView = viewHelp
			return m, nil
		case key.Matches(msg, keys.OpenExternal):
			return m, m.openCurrentItem()
		case key.Matches(msg, keys.Delete):
			m.deleteReturnView = viewDetail
			m.activeView = viewDeleteConfirm
			return m, nil
		case key.Matches(msg, keys.LinkItem):
			m.activeView = viewLinkSearch
			m.linkSearch.SetValue("")
			m.linkResults = nil
			m.linkSearch.Focus()
			return m, textinput.Blink
		case key.Matches(msg, keys.UnlinkItem):
			if m.cursor < len(m.items) && len(m.items[m.cursor].Links) > 0 {
				m.activeView = viewUnlinkSelect
				m.unlinkCursor = 0
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return m, cmd
		}
	}

	// List view
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, keys.Up):
		if m.cursor > 0 {
			m.cursor--
			if m.cursor < m.offset {
				m.offset = m.cursor
			}
		}
	case key.Matches(msg, keys.Down):
		if m.cursor < len(m.items)-1 {
			m.cursor++
			if m.cursor >= m.offset+m.listRows {
				m.offset = m.cursor - m.listRows + 1
			}
		}
	case key.Matches(msg, keys.Enter):
		if len(m.items) > 0 {
			m.activeView = viewDetail
			m.detail.SetContent(m.renderDetail(&m.items[m.cursor], m.width))
			m.detail.GotoTop()
		}
	case key.Matches(msg, keys.Search):
		m.searching = true
		m.search.Focus()
		return m, textinput.Blink
	case key.Matches(msg, keys.Clear):
		m.search.SetValue("")
		m.filter = model.ItemFilter{Limit: 100}
		return m, m.fetchItems()
	case key.Matches(msg, keys.FilterURL):
		return m, m.toggleTypeFilter(model.TypeURL)
	case key.Matches(msg, keys.FilterSnippet):
		return m, m.toggleTypeFilter(model.TypeSnippet)
	case key.Matches(msg, keys.FilterFile):
		return m, m.toggleTypeFilter(model.TypeFile)
	case key.Matches(msg, keys.FilterImage):
		return m, m.toggleTypeFilter(model.TypeImage)
	case key.Matches(msg, keys.FilterEmail):
		return m, m.toggleTypeFilter(model.TypeEmail)
	case key.Matches(msg, keys.Help):
		m.activeView = viewHelp
		return m, nil
	case key.Matches(msg, keys.Delete):
		if len(m.items) > 0 {
			m.deleteReturnView = viewList
			m.activeView = viewDeleteConfirm
		}
		return m, nil
	case key.Matches(msg, keys.Refresh):
		return m, m.fetchItems()
	}

	return m, nil
}

func (m *Model) parseSearch(input string) {
	m.filter.Query = ""
	m.filter.Tags = nil
	for _, token := range strings.Fields(input) {
		if after, ok := strings.CutPrefix(token, "tag:"); ok {
			m.filter.Tags = append(m.filter.Tags, after)
		} else {
			if m.filter.Query != "" {
				m.filter.Query += " "
			}
			m.filter.Query += token
		}
	}
}

func (m *Model) toggleTypeFilter(t model.ItemType) tea.Cmd {
	if m.filter.Type == t {
		m.filter.Type = ""
	} else {
		m.filter.Type = t
	}
	return m.fetchItems()
}

func (m Model) fetchItems() tea.Cmd {
	return func() tea.Msg {
		var items []model.Item
		var err error
		if m.filter.Query != "" {
			items, err = m.store.SearchItems(context.Background(), m.filter)
		} else {
			items, err = m.store.ListItems(context.Background(), m.filter)
		}
		if err != nil {
			return errMsg(err)
		}
		return itemsMsg(items)
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	switch m.activeView {
	case viewDetail:
		return m.viewDetail()
	case viewLinkSearch:
		return m.viewLinkSearch()
	case viewLinkLabel:
		return m.viewLinkLabel()
	case viewUnlinkSelect:
		return m.viewUnlinkSelect()
	case viewDeleteConfirm:
		return m.viewDeleteConfirm()
	case viewHelp:
		return m.viewHelp()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	var b strings.Builder

	// Header
	header := headerStyle.Width(m.width).Render("  stash — Personal Knowledge Vault")
	b.WriteString(header)
	b.WriteString("\n")

	// Search bar
	if m.searching {
		b.WriteString("  " + m.search.View())
	} else {
		query := m.search.Value()
		if query == "" {
			query = dimStyle.Render("/ to search")
		} else {
			query = searchStyle.Render("  " + query)
		}
		b.WriteString("  " + query)
	}

	// Active filter indicators
	if m.filter.Type != "" {
		b.WriteString("  " + filterStyle.Render(string(m.filter.Type)))
	}
	for _, tag := range m.filter.Tags {
		b.WriteString("  " + tagStyle.Render("tag:"+tag))
	}
	b.WriteString("\n")

	// Item list
	if m.err != nil {
		b.WriteString(errStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
		b.WriteString("\n")
	} else if len(m.items) == 0 {
		b.WriteString(dimStyle.Render("  No items found."))
		b.WriteString("\n")
	} else {
		end := m.offset + m.listRows
		if end > len(m.items) {
			end = len(m.items)
		}
		for i := m.offset; i < end; i++ {
			item := &m.items[i]
			selected := i == m.cursor

			line := formatListItem(item, m.width-4, selected)
			if selected {
				b.WriteString(selectedStyle.Render(line))
			} else {
				b.WriteString("  " + line)
			}
			b.WriteString("\n")
		}
	}

	// Status bar
	status := m.statusBar()
	// Pad to fill remaining height
	used := strings.Count(b.String(), "\n")
	for i := used; i < m.height-1; i++ {
		b.WriteString("\n")
	}
	b.WriteString(statusStyle.Width(m.width).Render(status))

	return b.String()
}

func (m Model) viewDetail() string {
	header := headerStyle.Width(m.width).Render("  Item Detail  (esc:back  o:open  d:delete  l:link  u:unlink)")
	return header + "\n" + m.detail.View()
}

func (m Model) viewLinkSearch() string {
	header := headerStyle.Width(m.width).Render("  Link Item  (tab/arrows:navigate  enter:select  esc:cancel)")
	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString("  " + m.linkSearch.View() + "\n\n")
	if len(m.linkResults) > 0 {
		for i, it := range m.linkResults {
			if i >= 8 {
				break
			}
			id := it.ID
			if len(id) > 10 {
				id = id[:10]
			}
			line := fmt.Sprintf(" %s %s  %s", typeIcon(it.Type), it.Title, dimStyle.Render(id))
			if i == m.linkCursor {
				b.WriteString(selectedStyle.Render(line))
			} else {
				b.WriteString("  " + line)
			}
			b.WriteString("\n")
		}
	} else if m.linkSearch.Value() != "" {
		b.WriteString(dimStyle.Render("  No results.") + "\n")
	}
	return b.String()
}

func (m Model) viewLinkLabel() string {
	header := headerStyle.Width(m.width).Render("  Link Label  (Enter to confirm, esc to cancel)")
	return header + "\n  " + m.linkLabel.View() + "\n"
}

func (m Model) viewUnlinkSelect() string {
	header := headerStyle.Width(m.width).Render("  Unlink Item  (Enter to remove, esc to cancel)")
	var b strings.Builder
	b.WriteString(header + "\n")
	if m.cursor < len(m.items) {
		item := &m.items[m.cursor]
		for i, lk := range item.Links {
			arrow := "\u2194"
			switch lk.Direction {
			case "outgoing":
				arrow = "\u2192"
			case "incoming":
				arrow = "\u2190"
			}
			label := ""
			if lk.Label != "" {
				label = " (" + lk.Label + ")"
			}
			id := lk.ItemID
			if len(id) > 10 {
				id = id[:10]
			}
			prefix := "  "
			if i == m.unlinkCursor {
				prefix = selectedStyle.Render("")
			}
			b.WriteString(fmt.Sprintf("%s %s [%s] %s %s%s\n", prefix, arrow, id, typeIcon(lk.Type), lk.Title, label))
		}
	}
	return b.String()
}

func (m Model) viewHelp() string {
	header := headerStyle.Width(m.width).Render("  Help  (press any key to dismiss)")
	var b strings.Builder
	b.WriteString(header + "\n\n")

	helpItems := []struct{ key, desc string }{
		{"/", "Search (supports tag:name filter syntax)"},
		{"1-5", "Filter by type (urls, snippets, files, images, emails)"},
		{"j/k or ↑/↓", "Navigate items"},
		{"enter", "View item detail"},
		{"o", "Open item in default application"},
		{"d", "Delete item (with confirmation)"},
		{"l", "Link current item to another"},
		{"u", "Unlink a linked item"},
		{"r", "Refresh item list"},
		{"ctrl+l", "Clear search and filters"},
		{"q", "Quit / back"},
		{"ctrl+c", "Force quit"},
		{"?", "Show this help"},
	}

	for _, h := range helpItems {
		k := detailLabel.Render(fmt.Sprintf("  %-14s", h.key))
		b.WriteString(k + h.desc + "\n")
	}

	b.WriteString("\n" + dimStyle.Render("  Search also matches tag names.") + "\n")

	return b.String()
}

func (m Model) viewDeleteConfirm() string {
	title := "(none)"
	if m.cursor < len(m.items) {
		title = m.items[m.cursor].Title
	}
	header := headerStyle.Width(m.width).Render("  Delete Item")
	return header + "\n\n" +
		errStyle.Render(fmt.Sprintf("  Delete \"%s\"?", title)) + "\n\n" +
		"  Press " + detailLabel.Render("y") + " to confirm, any other key to cancel.\n"
}

func (m Model) deleteCurrentItem() tea.Cmd {
	return func() tea.Msg {
		if m.cursor >= len(m.items) {
			return deleteDoneMsg{err: fmt.Errorf("no current item")}
		}
		err := m.store.DeleteItem(context.Background(), m.items[m.cursor].ID)
		return deleteDoneMsg{err: err}
	}
}

func (m Model) statusBar() string {
	left := fmt.Sprintf(" %d items", len(m.items))
	right := " /:search  1-5:filter  r:refresh  o:open  d:delete  ?:help  q:quit "
	gap := m.width - len(left) - len(right)
	if gap < 0 {
		gap = 0
	}
	return left + strings.Repeat(" ", gap) + right
}

func formatListItem(item *model.Item, width int, selected bool) string {
	icon := typeIcon(item.Type)
	title := item.Title
	if title == "" {
		title = "(untitled)"
	}

	tags := ""
	if len(item.Tags) > 0 {
		names := make([]string, len(item.Tags))
		for i, t := range item.Tags {
			names[i] = t.Name
		}
		tags = " " + tagStyle.Render("["+strings.Join(names, ", ")+"]")
	}

	age := relTime(item.CreatedAt)
	ageStr := dimStyle.Render(age)

	// Truncate title to fit
	maxTitle := width - len(icon) - len(age) - 6
	if len(item.Tags) > 0 {
		tagLen := 0
		for _, t := range item.Tags {
			tagLen += len(t.Name) + 2
		}
		maxTitle -= tagLen + 3
	}
	if maxTitle < 10 {
		maxTitle = 10
	}
	if len(title) > maxTitle {
		title = title[:maxTitle-3] + "..."
	}

	_ = selected
	return fmt.Sprintf("%s %s%s  %s", icon, title, tags, ageStr)
}

func typeIcon(t model.ItemType) string {
	switch t {
	case model.TypeURL:
		return urlTypeStyle.Render("URL")
	case model.TypeSnippet:
		return snippetStyle.Render("SNP")
	case model.TypeFile:
		return fileStyle.Render("FIL")
	case model.TypeImage:
		return imageStyle.Render("IMG")
	case model.TypeEmail:
		return emailStyle.Render("EML")
	default:
		return "???"
	}
}

func (m *Model) openCurrentItem() tea.Cmd {
	if m.cursor >= len(m.items) {
		return nil
	}
	item := &m.items[m.cursor]
	target := ""
	switch item.Type {
	case model.TypeURL:
		target = item.URL
	case model.TypeFile, model.TypeImage, model.TypeEmail:
		if item.StorePath != "" && m.files != nil {
			storePath := m.files.Path(item.StorePath)
			// Copy to temp with correct extension so OS opens with the right app
			ext := extFromMIMEOrSource(item.MimeType, item.SourcePath)
			if ext != "" {
				tmpFile := filepath.Join(os.TempDir(), "stash-open-"+item.StorePath[:8]+ext)
				if err := copyFile(storePath, tmpFile); err == nil {
					target = tmpFile
				} else {
					target = storePath
				}
			} else {
				target = storePath
			}
		} else if item.SourcePath != "" {
			target = item.SourcePath
		}
	}
	if target == "" {
		return nil
	}

	// For archives, extract to temp dir and open that
	if isArchiveMIME(item.MimeType) {
		return func() tea.Msg {
			dir, err := extractArchiveToTemp(target, item.MimeType)
			if err != nil {
				return openDoneMsg{err: fmt.Errorf("extract archive: %w", err)}
			}
			cmd := exec.Command("open", dir)
			return openDoneMsg{err: cmd.Start()}
		}
	}

	// Check for configured image viewer
	if item.Type == model.TypeImage {
		if viewer := config.Get().ImageViewer; viewer != "" {
			return func() tea.Msg {
				cmd := exec.Command(viewer, target)
				return openDoneMsg{err: cmd.Start()}
			}
		}
	}

	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", target)
		case "linux":
			cmd = exec.Command("xdg-open", target)
		case "windows":
			cmd = exec.Command("cmd", "/c", "start", target)
		default:
			return openDoneMsg{err: fmt.Errorf("unsupported platform: %s", runtime.GOOS)}
		}
		return openDoneMsg{err: cmd.Start()}
	}
}

func (m Model) searchForLink(query string) tea.Cmd {
	return func() tea.Msg {
		items, err := m.store.SearchItems(context.Background(), model.ItemFilter{Query: query, Limit: 10})
		if err != nil {
			return errMsg(err)
		}
		// Filter out the current item
		var filtered []model.Item
		currentID := ""
		if m.cursor < len(m.items) {
			currentID = m.items[m.cursor].ID
		}
		for _, it := range items {
			if it.ID != currentID {
				filtered = append(filtered, it)
			}
		}
		return linkSearchMsg(filtered)
	}
}

func (m Model) createLink(targetID, label string) tea.Cmd {
	return func() tea.Msg {
		if m.cursor >= len(m.items) {
			return linkDoneMsg{err: fmt.Errorf("no current item")}
		}
		err := m.store.LinkItems(context.Background(), m.items[m.cursor].ID, targetID, label, false)
		return linkDoneMsg{err: err}
	}
}

func (m Model) removeLink(targetID string) tea.Cmd {
	return func() tea.Msg {
		if m.cursor >= len(m.items) {
			return unlinkDoneMsg{err: fmt.Errorf("no current item")}
		}
		err := m.store.UnlinkItems(context.Background(), m.items[m.cursor].ID, targetID)
		return unlinkDoneMsg{err: err}
	}
}

func (m Model) refreshCurrentItem() tea.Cmd {
	return func() tea.Msg {
		if m.cursor >= len(m.items) {
			return nil
		}
		item, err := m.store.GetItem(context.Background(), m.items[m.cursor].ID)
		if err != nil {
			return errMsg(err)
		}
		return refreshedItemMsg{item: item}
	}
}

func (m *Model) renderDetail(item *model.Item, width int) string {
	var b strings.Builder

	b.WriteString(detailLabel.Render("ID:       ") + item.ID + "\n")
	b.WriteString(detailLabel.Render("Type:     ") + item.Type.Display() + "\n")
	b.WriteString(detailLabel.Render("Title:    ") + item.Title + "\n")
	if item.URL != "" {
		b.WriteString(detailLabel.Render("URL:      ") + urlStyle.Render(item.URL) + "\n")
	}
	if item.Notes != "" {
		b.WriteString(detailLabel.Render("Notes:    ") + item.Notes + "\n")
	}
	if item.MimeType != "" {
		b.WriteString(detailLabel.Render("MIME:     ") + item.MimeType + "\n")
	}
	if item.FileSize > 0 {
		b.WriteString(detailLabel.Render("Size:     ") + humanSize(item.FileSize) + "\n")
	}
	if item.SourcePath != "" {
		b.WriteString(detailLabel.Render("Source:   ") + item.SourcePath + "\n")
	}
	if len(item.Tags) > 0 {
		names := make([]string, len(item.Tags))
		for i, t := range item.Tags {
			names[i] = t.Name
		}
		b.WriteString(detailLabel.Render("Tags:     ") + strings.Join(names, ", ") + "\n")
	}
	if len(item.Collections) > 0 {
		names := make([]string, len(item.Collections))
		for i, c := range item.Collections {
			names[i] = c.Name
		}
		b.WriteString(detailLabel.Render("Colls:    ") + strings.Join(names, ", ") + "\n")
	}
	if len(item.Links) > 0 {
		b.WriteString(detailLabel.Render("Links:") + "\n")
		for _, lk := range item.Links {
			arrow := "\u2194" // ↔
			switch lk.Direction {
			case "outgoing":
				arrow = "\u2192" // →
			case "incoming":
				arrow = "\u2190" // ←
			}
			label := ""
			if lk.Label != "" {
				label = " (" + lk.Label + ")"
			}
			id := lk.ItemID
			if len(id) > 10 {
				id = id[:10]
			}
			b.WriteString(fmt.Sprintf("  %s [%s] %s %s%s\n", arrow, id, typeIcon(lk.Type), lk.Title, label))
		}
	}
	b.WriteString(detailLabel.Render("Created:  ") + item.CreatedAt.Format(time.RFC3339) + "\n")
	b.WriteString(detailLabel.Render("Updated:  ") + item.UpdatedAt.Format(time.RFC3339) + "\n")

	// Image preview
	if item.Type == model.TypeImage && item.StorePath != "" && m.files != nil {
		b.WriteString("\n")
		filePath := m.files.Path(item.StorePath)
		if supportsGraphics() {
			if img, err := renderImage(filePath, width/2, 25); err == nil {
				b.WriteString(img)
			} else {
				b.WriteString(fallbackText() + "\n")
			}
		} else {
			b.WriteString(fallbackText() + "\n")
		}
	}

	// Archive contents tree
	if isArchiveMIME(item.MimeType) && item.StorePath != "" && m.files != nil {
		archivePath := m.files.Path(item.StorePath)
		if tree := renderArchiveTree(archivePath, item.MimeType); tree != "" {
			b.WriteString("\n")
			b.WriteString(detailLabel.Render("--- Archive Contents ---") + "\n")
			b.WriteString(tree)
		}
	}

	if item.ExtractedText != "" && !isArchiveMIME(item.MimeType) {
		b.WriteString("\n")
		b.WriteString(detailLabel.Render("--- Extracted Text ---") + "\n")
		text := item.ExtractedText
		if len(text) > 2000 {
			text = text[:2000] + "\n...(truncated)"
		}
		b.WriteString(text + "\n")
	}

	return b.String()
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

func extFromMIMEOrSource(mimeType, sourcePath string) string {
	if sourcePath != "" {
		if ext := filepath.Ext(sourcePath); ext != "" {
			return ext
		}
	}
	switch {
	case mimeType == "application/pdf":
		return ".pdf"
	case mimeType == "text/html":
		return ".html"
	case mimeType == "text/plain":
		return ".txt"
	case mimeType == "image/png":
		return ".png"
	case mimeType == "image/jpeg":
		return ".jpg"
	case mimeType == "image/gif":
		return ".gif"
	case mimeType == "image/webp":
		return ".webp"
	case mimeType == "application/gzip":
		return ".tar.gz"
	case mimeType == "application/zip":
		return ".zip"
	default:
		return ""
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func isArchiveMIME(mimeType string) bool {
	return strings.Contains(mimeType, "gzip") ||
		strings.Contains(mimeType, "tar") ||
		strings.Contains(mimeType, "zip")
}

type archiveEntry struct {
	name  string
	isDir bool
}

func listTarGzEntries(path string) ([]archiveEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	var entries []archiveEntry
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, archiveEntry{
			name:  hdr.Name,
			isDir: hdr.Typeflag == tar.TypeDir,
		})
	}
	return entries, nil
}

type treeNode struct {
	name     string
	isDir    bool
	children []*treeNode
	childMap map[string]*treeNode
}

func renderArchiveTree(path, mimeType string) string {
	var entries []archiveEntry
	var err error

	if strings.Contains(mimeType, "gzip") || strings.Contains(mimeType, "tar") {
		entries, err = listTarGzEntries(path)
	}
	if err != nil || len(entries) == 0 {
		return ""
	}

	root := &treeNode{childMap: make(map[string]*treeNode)}
	for _, e := range entries {
		parts := strings.Split(strings.TrimSuffix(e.name, "/"), "/")
		cur := root
		for i, p := range parts {
			if p == "" {
				continue
			}
			child, ok := cur.childMap[p]
			if !ok {
				child = &treeNode{
					name:     p,
					isDir:    e.isDir || i < len(parts)-1,
					childMap: make(map[string]*treeNode),
				}
				cur.childMap[p] = child
				cur.children = append(cur.children, child)
			}
			if i == len(parts)-1 {
				child.isDir = e.isDir
			}
			cur = child
		}
	}

	var b strings.Builder
	sortChildren := func(nodes []*treeNode) {
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].isDir != nodes[j].isDir {
				return nodes[i].isDir
			}
			return nodes[i].name < nodes[j].name
		})
	}

	var walk func(n *treeNode, prefix string, last bool)
	walk = func(n *treeNode, prefix string, last bool) {
		connector := "├── "
		if last {
			connector = "└── "
		}
		if n.name != "" {
			label := n.name
			if n.isDir {
				label += "/"
			}
			b.WriteString(prefix + connector + label + "\n")
		}

		childPrefix := prefix
		if n.name != "" {
			if last {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
		}

		sortChildren(n.children)
		for i, child := range n.children {
			walk(child, childPrefix, i == len(n.children)-1)
		}
	}

	// If single top-level dir, show it as root
	if len(root.children) == 1 && root.children[0].isDir {
		top := root.children[0]
		b.WriteString(top.name + "/\n")
		sortChildren(top.children)
		for i, child := range top.children {
			walk(child, "", i == len(top.children)-1)
		}
	} else {
		sortChildren(root.children)
		for i, child := range root.children {
			walk(child, "", i == len(root.children)-1)
		}
	}

	return b.String()
}

func extractArchiveToTemp(archivePath, mimeType string) (string, error) {
	if !strings.Contains(mimeType, "gzip") && !strings.Contains(mimeType, "tar") {
		return "", fmt.Errorf("unsupported archive type: %s", mimeType)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gr.Close()

	dir, err := os.MkdirTemp("", "stash-archive-*")
	if err != nil {
		return "", err
	}

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return dir, err
		}

		target := filepath.Join(dir, filepath.Clean(hdr.Name))
		// Prevent path traversal
		if !strings.HasPrefix(target, dir) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return dir, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return dir, err
			}
			out, err := os.Create(target)
			if err != nil {
				return dir, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return dir, err
			}
			out.Close()
		}
	}
	return dir, nil
}
