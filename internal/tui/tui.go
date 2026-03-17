package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

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
)

// Model is the top-level bubbletea model.
type Model struct {
	store  store.Store
	width  int
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

	err error
}

type itemsMsg []model.Item
type errMsg error

// New creates a new TUI model.
func New(s store.Store) Model {
	ti := textinput.New()
	ti.Placeholder = "Search..."
	ti.CharLimit = 256

	return Model{
		store:  s,
		search: ti,
		filter: model.ItemFilter{Limit: 100},
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

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.searching {
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
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
	// Global keys
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	}

	// Search mode
	if m.searching {
		switch {
		case key.Matches(msg, keys.Enter):
			m.searching = false
			m.search.Blur()
			m.filter.Query = m.search.Value()
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

	// Detail view
	if m.activeView == viewDetail {
		switch {
		case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Back):
			m.activeView = viewList
			return m, nil
		default:
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return m, cmd
		}
	}

	// List view
	switch {
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
			m.detail.SetContent(renderDetail(&m.items[m.cursor], m.width))
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
	case key.Matches(msg, keys.FilterLink):
		return m, m.toggleTypeFilter(model.TypeLink)
	case key.Matches(msg, keys.FilterSnippet):
		return m, m.toggleTypeFilter(model.TypeSnippet)
	case key.Matches(msg, keys.FilterFile):
		return m, m.toggleTypeFilter(model.TypeFile)
	case key.Matches(msg, keys.FilterImage):
		return m, m.toggleTypeFilter(model.TypeImage)
	case key.Matches(msg, keys.Refresh):
		return m, m.fetchItems()
	}

	return m, nil
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

	if m.activeView == viewDetail {
		return m.viewDetail()
	}
	return m.viewList()
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

	// Active filter indicator
	if m.filter.Type != "" {
		b.WriteString("  " + filterStyle.Render(string(m.filter.Type)))
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
	header := headerStyle.Width(m.width).Render("  Item Detail  (esc to go back)")
	return header + "\n" + m.detail.View()
}

func (m Model) statusBar() string {
	left := fmt.Sprintf(" %d items", len(m.items))
	right := " /:search  1-4:filter  r:refresh  enter:detail  q:quit "
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
	case model.TypeLink:
		return linkStyle.Render("LNK")
	case model.TypeSnippet:
		return snippetStyle.Render("SNP")
	case model.TypeFile:
		return fileStyle.Render("FIL")
	case model.TypeImage:
		return imageStyle.Render("IMG")
	default:
		return "???"
	}
}

func renderDetail(item *model.Item, width int) string {
	var b strings.Builder

	b.WriteString(detailLabel.Render("ID:       ") + item.ID + "\n")
	b.WriteString(detailLabel.Render("Type:     ") + string(item.Type) + "\n")
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
	b.WriteString(detailLabel.Render("Created:  ") + item.CreatedAt.Format(time.RFC3339) + "\n")
	b.WriteString(detailLabel.Render("Updated:  ") + item.UpdatedAt.Format(time.RFC3339) + "\n")

	if item.ExtractedText != "" {
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
