package tui

import "github.com/charmbracelet/lipgloss"

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("236"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			PaddingLeft(1).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("62"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	searchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	filterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	errStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	tagStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("109"))

	linkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("75")).
			Bold(true)

	snippetStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("150")).
			Bold(true)

	fileStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	imageStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("213")).
			Bold(true)

	emailStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("222")).
			Bold(true)

	detailLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("62")).
			Bold(true)

	urlStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("75")).
			Underline(true)
)
