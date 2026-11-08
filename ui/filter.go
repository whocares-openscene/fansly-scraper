package ui

import (
	"strings"
	//"fmt"
	"github.com/agnosto/fansly-scraper/auth"
	//"github.com/agnosto/fansly-scraper/config"
	//"github.com/agnosto/fansly-scraper/core"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	//"github.com/charmbracelet/bubbles/key"
	//"github.com/charmbracelet/bubbles/table"
)

// HandleMainMenuUpdate handles updates when in the MainMenuState
func (m *MainModel) HandleFilterModelsMenuUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "esc":
			m.resetModelFilter()
			m.state = FollowedModelsState
			return m, nil
		case "up":
			//    m.table.MoveUp(1)
			return m, nil
		case "down":
			//    m.table.MoveDown(1)
			return m, nil
		case "enter":
			m.applyFilter()
			m.state = FollowedModelsState
			m.filterInput = ""
			return m, nil
		case "backspace":
			if len(m.filterInput) > 0 {
				m.filterInput = m.filterInput[:len(m.filterInput)-1]
				m.applyFilter()
			}
		default:
			m.filterInput += msg.String()
			m.applyFilter()
			return m, nil
		}
	}
	return m, nil
}

func (m *MainModel) RenderFilterModelsMenu() string {
	var sb strings.Builder

	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#f5c2e7")).Render(m.welcome) + "\n")
	sb.WriteString("Filter by username: " + m.filterInput + "\n")
	sb.WriteString(m.table.View() + "\n")

	return sb.String()
}

func (m *MainModel) applyFilter() {
	filtered := make([]auth.FollowedModel, 0, len(m.followedModels))
	for _, model := range m.followedModels {
		if m.modelMatchesFilter(model) {
			filtered = append(filtered, model)
		}
	}
	m.filteredModels = filtered
	m.updateTable()
}

// modelMatchesFilter reports whether a creator passes both the username text
// filter and the subscription filter.
func (m *MainModel) modelMatchesFilter(model auth.FollowedModel) bool {
	switch m.modelFilter {
	case modelFilterSubscribed:
		if !model.Subscribed {
			return false
		}
	case modelFilterNotSubscribed:
		if model.Subscribed {
			return false
		}
	}
	return strings.Contains(strings.ToLower(model.Username), strings.ToLower(m.filterInput))
}

// cycleModelFilter advances the subscription filter: all -> subscribed only ->
// not subscribed -> all. The username filter is kept.
func (m *MainModel) cycleModelFilter() {
	for i, mode := range modelFilterModes {
		if mode == m.modelFilter {
			m.modelFilter = modelFilterModes[(i+1)%len(modelFilterModes)]
			break
		}
	}
	m.applyFilter()
}

// resetModelFilter clears the username and subscription filters.
func (m *MainModel) resetModelFilter() {
	m.filterInput = ""
	m.modelFilter = modelFilterAll
	m.applyFilter()
}

// modelFilterLabel describes the active subscription filter, or "" when every
// creator is listed.
func (m *MainModel) modelFilterLabel() string {
	if m.modelFilter == modelFilterAll {
		return ""
	}
	return m.modelFilter.String()
}
