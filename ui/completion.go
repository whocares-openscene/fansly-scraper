package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	completionOptionMainMenu = "Return to main menu"
	completionOptionQuit     = "Quit"
)

// completionOptions lists the choices offered after a download finishes. The
// "another wall" entry only appears when the finished download was a wall
// download, so it is always clear which model the walls belong to.
func (m *MainModel) completionOptions() []string {
	options := []string{m.processAnotherText()}
	if m.actionChosen == "download" && m.lastDownloadWasWall {
		options = append(options, fmt.Sprintf("Download another wall from %s", m.selectedModel))
	}
	return append(options, completionOptionMainMenu, completionOptionQuit)
}

// wallsCompletionOption is the menu entry that reopens the wall picker for the
// model that was just downloaded.
func (m *MainModel) wallsCompletionOption() string {
	return fmt.Sprintf("Download another wall from %s", m.selectedModel)
}

func (m *MainModel) HandleCompletionUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		options := m.completionOptions()
		switch {
		case key.Matches(msg, m.keys.Up):
			m.cursorPos = (m.cursorPos - 1 + len(options)) % len(options)
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.cursorPos = (m.cursorPos + 1) % len(options)
			return m, nil
		case key.Matches(msg, m.keys.Select):
			if m.cursorPos < 0 || m.cursorPos >= len(options) {
				return m, nil
			}
			switch options[m.cursorPos] {
			case completionOptionMainMenu:
				m.state = MainMenuState
				m.cursorPos = 0
				return m, nil
			case completionOptionQuit:
				m.quit = true
				return m, tea.Quit
			case m.wallsCompletionOption():
				m.state = WallSelectionState
				m.cursorPos = 0
				m.updateWallTable()
				return m, nil
			default: // Process another
				if m.actionChosen == "download_individual_posts" {
					m.state = DownloadIndividualPostState
					m.postLinksInput.Reset()
					m.postLinksInput.Focus()
					m.cursorPos = 0
					return m, nil
				}
				if m.actionChosen == "explore" {
					m.state = ExploreState
					m.exploreInput.Reset()
					m.exploreInput.Focus()
					m.cursorPos = 0
					return m, nil
				}
				m.state = FollowedModelsState
				m.resetModelFilter()
				m.cursorPos = 0
				return m, nil
			}
		}
	}
	return m, nil
}

// processAnotherText describes the repeated-action entry for the flow that
// just finished.
func (m *MainModel) processAnotherText() string {
	switch m.actionChosen {
	case "download":
		return "Download another user's posts"
	case "download_individual_posts":
		return "Download more individual posts"
	case "explore":
		return "Explore more premium previews"
	case "download_purchases":
		return "Download more purchased content"
	case "monitor":
		return "Monitor another user's livestreams"
	case "like":
		return "Like another user's posts"
	case "unlike":
		return "Unlike another user's posts"
	default:
		return "Process another action"
	}
}

func (m *MainModel) RenderCompletionMenu() string {
	var sb strings.Builder

	options := m.completionOptions()

	sb.WriteString("Operation completed successfully!\n\n")

	if stats := m.lastStats; stats.Total() > 0 {
		sb.WriteString(fmt.Sprintf("Videos downloaded: %d\n", stats.VideosDownloaded))
		sb.WriteString(fmt.Sprintf("Photos downloaded: %d\n", stats.ImagesDownloaded))
		sb.WriteString(fmt.Sprintf("Videos copied:     %d\n", stats.VideosCopied))
		sb.WriteString(fmt.Sprintf("Photos copied:     %d\n", stats.ImagesCopied))
		sb.WriteString("\n")
	}

	sb.WriteString("What would you like to do next?\n\n")

	for i, opt := range options {
		if i == m.cursorPos {
			sb.WriteString("> " + lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb")).Render(opt) + "\n")
		} else {
			sb.WriteString("  " + opt + "\n")
		}
	}

	return sb.String()
}
