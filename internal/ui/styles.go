package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	cAccent  = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	cText    = lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"}
	cDim     = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}
	cFaint   = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"}
	cSelBg   = lipgloss.AdaptiveColor{Light: "#F3F0FF", Dark: "#1E1B2E"}
	cWorking = lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#FACC15"}
	cNeeds   = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"}
	cUnseen  = lipgloss.AdaptiveColor{Light: "#2563EB", Dark: "#60A5FA"}
	cReady   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	cExited  = lipgloss.AdaptiveColor{Light: "#A21CAF", Dark: "#C084FC"}
	cAdd     = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	cDel     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}

	sTitle    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sText     = lipgloss.NewStyle().Foreground(cText)
	sDim      = lipgloss.NewStyle().Foreground(cDim)
	sFaint    = lipgloss.NewStyle().Foreground(cFaint)
	sName     = lipgloss.NewStyle().Foreground(cText).Bold(true)
	sSelBar   = lipgloss.NewStyle().Foreground(cAccent)
	sWorking  = lipgloss.NewStyle().Foreground(cWorking)
	sNeeds    = lipgloss.NewStyle().Foreground(cNeeds).Bold(true)
	sReady    = lipgloss.NewStyle().Foreground(cReady)
	sUnseen   = lipgloss.NewStyle().Foreground(cUnseen).Bold(true)
	sPrompt   = lipgloss.NewStyle().Foreground(cDim).Italic(true)
	sExited   = lipgloss.NewStyle().Foreground(cExited)
	sAdd      = lipgloss.NewStyle().Foreground(cAdd)
	sDel      = lipgloss.NewStyle().Foreground(cDel)
	sKey      = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sSelected = lipgloss.NewStyle().Background(cSelBg)
)

func age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 10*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
