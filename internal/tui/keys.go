package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up            key.Binding
	Down          key.Binding
	Enter         key.Binding
	Escape        key.Binding
	Quit          key.Binding
	ForceQuit     key.Binding
	Search        key.Binding
	Clear         key.Binding
	Refresh       key.Binding
	OpenExternal  key.Binding
	Delete        key.Binding
	LinkItem      key.Binding
	UnlinkItem    key.Binding
	FilterURL     key.Binding
	FilterSnippet key.Binding
	FilterFile    key.Binding
	FilterImage   key.Binding
	FilterEmail   key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("up/k", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("down/j", "move down"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "view detail"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back/cancel"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit/back"),
	),
	ForceQuit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "force quit"),
	),
	Search: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "search"),
	),
	Clear: key.NewBinding(
		key.WithKeys("ctrl+l"),
		key.WithHelp("ctrl+l", "clear search"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	OpenExternal: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open externally"),
	),
	Delete: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "delete item"),
	),
	LinkItem: key.NewBinding(
		key.WithKeys("l"),
		key.WithHelp("l", "link item"),
	),
	UnlinkItem: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "unlink item"),
	),
	FilterURL: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "urls"),
	),
	FilterSnippet: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "snippets"),
	),
	FilterFile: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "files"),
	),
	FilterImage: key.NewBinding(
		key.WithKeys("4"),
		key.WithHelp("4", "images"),
	),
	FilterEmail: key.NewBinding(
		key.WithKeys("5"),
		key.WithHelp("5", "emails"),
	),
}
