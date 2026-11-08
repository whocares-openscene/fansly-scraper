package ui

import (
	"strings"

	"github.com/agnosto/fansly-scraper/logger"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HandleExploreUpdate handles updates when in the ExploreState (tag input).
func (m *MainModel) HandleExploreUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			m.quit = true
			return m, tea.Quit
		case key.Matches(msg, m.keys.Back):
			m.state = MainMenuState
			m.cursorPos = 0
			return m, nil
		case key.Matches(msg, m.keys.Select):
			val := strings.TrimSpace(m.exploreInput.Value())
			if val != "" {
				m.state = DownloadProgressState
				return m, m.startExploreDownload(val)
			}
			return m, nil
		}
	}

	m.exploreInput, cmd = m.exploreInput.Update(msg)
	return m, cmd
}

// RenderExploreMenu renders the tag-input screen for the Explore feature.
func (m *MainModel) RenderExploreMenu() string {
	var sb strings.Builder

	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Bold(true).Render("Explore Premium Previews") + "\n\n")
	sb.WriteString("Enter one or more tags to explore and download premium post previews.\n")
	sb.WriteString("Separate multiple tags with commas. A leading # is optional.\n")
	sb.WriteString("To keep only specific creators, add \"@ user1,user2\" after the tags.\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render("Example: pregnant @ testuser") + "\n\n")

	sb.WriteString(m.exploreInput.View() + "\n\n")

	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render("Press Enter to start, Esc to cancel / return to menu") + "\n")

	return sb.String()
}

// parseExploreInput splits the raw input into tags and an optional creator
// filter. Everything before the first '@' is a comma-separated tag list;
// everything after it is a comma-separated username filter.
func parseExploreInput(input string) (tags []string, users []string) {
	tagPart := input
	userPart := ""
	if idx := strings.Index(input, "@"); idx >= 0 {
		tagPart = input[:idx]
		userPart = input[idx+1:]
	}
	for _, t := range strings.Split(tagPart, ",") {
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
		if t != "" {
			tags = append(tags, t)
		}
	}
	for _, u := range strings.Split(userPart, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			users = append(users, u)
		}
	}
	return tags, users
}

// startExploreDownload runs the Explore download asynchronously.
func (m *MainModel) startExploreDownload(input string) tea.Cmd {
	ctx, job := m.newDownloadJob()

	return func() tea.Msg {
		tags, users := parseExploreInput(input)
		if len(tags) == 0 {
			logger.Logger.Printf("Explore: no valid tags in input %q", input)
			return downloadCompleteMsg{job: job, stats: m.downloadStats()}
		}
		if err := m.downloader.DownloadExplore(ctx, tags, users, m.downloader.PostLimit()); err != nil {
			logger.Logger.Printf("Explore download failed: %v", err)
			return downloadErrorMsg{Error: err, job: job}
		}
		return downloadCompleteMsg{job: job, stats: m.downloadStats()}
	}
}
