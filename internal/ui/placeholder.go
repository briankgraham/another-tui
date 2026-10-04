package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ctabs/internal/app"
)

// Placeholder fills the stage when no session is shown.
type Placeholder struct{ w, h int }

func (p Placeholder) Init() tea.Cmd { return nil }

func (p Placeholder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(tea.WindowSizeMsg); ok {
		p.w, p.h = m.Width, m.Height
	}
	return p, nil
}

func (p Placeholder) View() string {
	body := lipgloss.JoinVertical(lipgloss.Center,
		sTitle.Render("ctabs"),
		"",
		sDim.Render("parallel Claude sessions, one worktree each"),
		"",
		sKey.Render(app.LeaderLabel())+sDim.Render(" menu from anywhere: new session, switch, detach, quit"),
	)
	return lipgloss.Place(p.w, p.h, lipgloss.Center, lipgloss.Center, body)
}
