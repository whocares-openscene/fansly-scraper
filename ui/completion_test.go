package ui

import (
	"strings"
	"testing"

	"github.com/agnosto/fansly-scraper/auth"
	"github.com/agnosto/fansly-scraper/download"
	tea "github.com/charmbracelet/bubbletea"
)

func wallDownloadModel() *MainModel {
	return &MainModel{
		actionChosen:        "download",
		lastDownloadWasWall: true,
		selectedModel:       "graciemontana",
		selectedModelId:     "model-1",
		followedModels: []auth.FollowedModel{{
			ID:       "model-1",
			Username: "graciemontana",
			Walls: []auth.Wall{
				{ID: "wall-2", Name: "FEET", Pos: 2},
				{ID: "wall-1", Name: "FACE", Pos: 1},
			},
		}},
	}
}

func TestCompletionMenuOffersAnotherWallAfterWallDownload(t *testing.T) {
	m := wallDownloadModel()

	out := m.RenderCompletionMenu()
	for _, want := range []string{
		"Download another user's posts",
		"Download another wall from graciemontana",
		"Return to main menu",
		"Quit",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("completion menu is missing %q:\n%s", want, out)
		}
	}
}

func TestCompletionMenuHidesWallOptionForPlainDownloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		wasWall bool
	}{
		{name: "plain timeline download", action: "download"},
		{name: "previous wall, new like action", action: "like", wasWall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := wallDownloadModel()
			m.actionChosen = tc.action
			m.lastDownloadWasWall = tc.wasWall

			if out := m.RenderCompletionMenu(); strings.Contains(out, "another wall") {
				t.Fatalf("completion menu unexpectedly offers another wall:\n%s", out)
			}
		})
	}
}

func TestCompletionWallOptionReopensWallSelection(t *testing.T) {
	m := wallDownloadModel()
	m.keys = defaultKeyMap
	// The wall entry sits directly after the repeated-action entry.
	m.cursorPos = 1

	next, cmd := m.HandleCompletionUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("reopening wall selection must not start a command")
	}
	if next.(*MainModel).state != WallSelectionState {
		t.Fatalf("expected wall selection, got state %v", next.(*MainModel).state)
	}

	rows := m.table.Rows()
	if len(rows) != 2 {
		t.Fatalf("expected 2 wall rows, got %d", len(rows))
	}
	if rows[0][1] != "wall-1" || rows[1][1] != "wall-2" {
		t.Fatalf("walls are not sorted by position: %+v", rows)
	}
	if m.cursorPos != 0 {
		t.Fatalf("cursor should reset to the first wall, got %d", m.cursorPos)
	}
}

func TestRenderCompletionMenuShowsDownloadStats(t *testing.T) {
	m := &MainModel{
		actionChosen: "download",
		lastStats: download.SessionStats{
			VideosDownloaded: 3,
			ImagesDownloaded: 4,
			VideosCopied:     1,
			ImagesCopied:     2,
		},
	}

	out := m.RenderCompletionMenu()
	for _, want := range []string{
		"Operation completed successfully!",
		"Videos downloaded: 3",
		"Photos downloaded: 4",
		"Videos copied:     1",
		"Photos copied:     2",
		"What would you like to do next?",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("completion menu is missing %q:\n%s", want, out)
		}
	}
}

func TestRenderCompletionMenuHidesStatsWithoutActivity(t *testing.T) {
	// Actions that do not download anything (like/unlike) must not show the
	// counters left over from an earlier download.
	m := &MainModel{actionChosen: "like"}

	out := m.RenderCompletionMenu()
	for _, unwanted := range []string{"Videos downloaded", "Photos downloaded", "Videos copied", "Photos copied"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("completion menu unexpectedly contains %q:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "Operation completed successfully!") {
		t.Fatalf("completion menu lost its heading:\n%s", out)
	}
}
