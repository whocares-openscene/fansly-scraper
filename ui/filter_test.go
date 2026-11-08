package ui

import (
	"strings"
	"testing"

	"github.com/agnosto/fansly-scraper/auth"
	tea "github.com/charmbracelet/bubbletea"
)

func modelListWithSubscriptions() *MainModel {
	m := NewMainModel(nil, "test", nil)
	m.width = 120
	m.height = 40
	m.state = FollowedModelsState
	m.followedModels = []auth.FollowedModel{
		{ID: "1", Username: "alpha", Subscribed: true},
		{ID: "2", Username: "beta", Subscribed: true},
		{ID: "3", Username: "gamma", Subscribed: false},
	}
	m.resetModelFilter()
	return m
}

func filteredUsernames(m *MainModel) []string {
	names := make([]string, len(m.filteredModels))
	for i, model := range m.filteredModels {
		names[i] = model.Username
	}
	return names
}

func pressKey(m *MainModel, key string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func TestSubscriptionFilterCyclesThroughModes(t *testing.T) {
	m := modelListWithSubscriptions()

	if got := filteredUsernames(m); len(got) != 3 {
		t.Fatalf("expected all 3 creators initially, got %v", got)
	}
	if m.modelFilterLabel() != "" {
		t.Fatalf("unfiltered list should have no label, got %q", m.modelFilterLabel())
	}

	pressKey(m, "s")
	if m.modelFilter != modelFilterSubscribed {
		t.Fatalf("first toggle should select subscribed only, got %v", m.modelFilter)
	}
	if got := filteredUsernames(m); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("subscribed filter returned %v", got)
	}

	pressKey(m, "s")
	if m.modelFilter != modelFilterNotSubscribed {
		t.Fatalf("second toggle should select not subscribed, got %v", m.modelFilter)
	}
	if got := filteredUsernames(m); len(got) != 1 || got[0] != "gamma" {
		t.Fatalf("not-subscribed filter returned %v", got)
	}

	pressKey(m, "s")
	if m.modelFilter != modelFilterAll {
		t.Fatalf("third toggle should return to all, got %v", m.modelFilter)
	}
	if got := filteredUsernames(m); len(got) != 3 {
		t.Fatalf("expected all creators again, got %v", got)
	}
}

func TestSubscriptionFilterCombinesWithUsernameFilter(t *testing.T) {
	m := modelListWithSubscriptions()
	m.modelFilter = modelFilterSubscribed
	m.filterInput = "alp"
	m.applyFilter()

	if got := filteredUsernames(m); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("combined filter returned %v", got)
	}

	// Typing a name that only matches the unsubscribed creator must yield nothing.
	m.filterInput = "gam"
	m.applyFilter()
	if got := filteredUsernames(m); len(got) != 0 {
		t.Fatalf("combined filter leaked unsubscribed creators: %v", got)
	}
}

func TestModelListShowsSubscriptionColumn(t *testing.T) {
	m := modelListWithSubscriptions()

	rows := m.table.Rows()
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if len(rows[0]) != 8 {
		t.Fatalf("expected 8 columns (indicator, username, sub, 5 stats), got %d", len(rows[0]))
	}
	if rows[0][1] != "alpha" || rows[0][2] != "yes" {
		t.Fatalf("unexpected first row: %v", rows[0])
	}
	if rows[2][1] != "gamma" || rows[2][2] != "-" {
		t.Fatalf("unsubscribed creator should be marked '-': %v", rows[2])
	}
}

func TestFilterLabelIsRendered(t *testing.T) {
	m := modelListWithSubscriptions()
	m.modelFilter = modelFilterSubscribed
	m.applyFilter()

	out := m.RenderFollowedModelsMenu()
	if !strings.Contains(out, "Showing subscribed only") {
		t.Fatalf("render is missing the filter label:\n%s", out)
	}
}

func TestResetModelFilterClearsBothFilters(t *testing.T) {
	m := modelListWithSubscriptions()
	m.modelFilter = modelFilterSubscribed
	m.filterInput = "alp"
	m.applyFilter()

	// "r" resets the list.
	pressKey(m, "r")
	if m.modelFilter != modelFilterAll || m.filterInput != "" {
		t.Fatalf("reset left filters behind: mode=%v input=%q", m.modelFilter, m.filterInput)
	}
	if got := filteredUsernames(m); len(got) != 3 {
		t.Fatalf("reset left a filtered list: %v", got)
	}
}
