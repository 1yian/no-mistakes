package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const (
	ansiRed         = "1"
	ansiGreen       = "2"
	ansiYellow      = "3"
	ansiBlue        = "4"
	ansiCyan        = "6"
	ansiBrightBlack = "8"
)

func init() {
	configureTUIColors()
}

func configureTUIColors() {
	lipgloss.SetColorProfile(termenv.ANSI)
}
