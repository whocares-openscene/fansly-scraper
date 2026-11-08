package ui

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agnosto/fansly-scraper/auth"
	"github.com/agnosto/fansly-scraper/core"
	"github.com/agnosto/fansly-scraper/download"
	"github.com/agnosto/fansly-scraper/service"

	"context"

	"github.com/schollz/progressbar/v3"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"

	//"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	//"github.com/charmbracelet/lipgloss"
)

type AppState int

const (
	MainMenuState AppState = iota
	FollowedModelsState
	DownloadState
	DownloadActionsState
	LiveMonitorState
	LikeUnlikeState
	FilterState
	DownloadProgressState
	LiveMonitorFilterState
	DownloadPurchasedState
	CompletionState
	LoadingState
	DonateState
	WallSelectionState
	DownloadIndividualPostState
	ExploreState
)

// modelFilterMode selects which creators the model list shows.
type modelFilterMode int

const (
	modelFilterAll modelFilterMode = iota
	modelFilterSubscribed
	modelFilterNotSubscribed
)

// modelFilterModes is the order the "s" key cycles through.
var modelFilterModes = []modelFilterMode{
	modelFilterAll,
	modelFilterSubscribed,
	modelFilterNotSubscribed,
}

// String describes the mode for the list header.
func (f modelFilterMode) String() string {
	switch f {
	case modelFilterSubscribed:
		return "subscribed only"
	case modelFilterNotSubscribed:
		return "not subscribed"
	default:
		return "all creators"
	}
}

type MainModel struct {
	version                   string
	quit                      bool
	cursorPos                 int
	selected                  string
	options                   []string
	state                     AppState
	followedModels            []auth.FollowedModel
	filteredModels            []auth.FollowedModel
	filterInput               string
	modelFilter               modelFilterMode
	viewportStart             int
	viewportSize              int
	welcome                   string
	table                     table.Model
	monitoringTable           table.Model
	liveMonitorFilterInput    string
	filteredLiveMonitorModels []auth.FollowedModel
	keys                      keyMap
	help                      help.Model
	width                     int
	height                    int
	actionChosen              string
	downloadOptions           []string
	selectedOption            string
	selectedModel             string
	selectedModelId           string
	downloader                *download.Downloader
	progressBar               *progressbar.ProgressBar
	cancelDownload            context.CancelFunc
	downloadJob               int // identifies the current download; results from older jobs are ignored
	message                   string
	monitoredModels           map[string]bool // Map of model IDs to monitoring status
	monitoringService         *service.MonitoringService
	program                   *tea.Program
	UpdateAvailable           bool
	LatestVersion             string
	isLoading                 bool
	loadingMessage            string
	loadingDots               int
	loadingTicker             *time.Ticker
	accountsFetched           bool
	selectedWallID            string
	selectedWallName          string
	postLinksInput            textinput.Model
	exploreInput              textinput.Model
	liveStatuses              map[string]bool // model ID -> live, cached for the monitor table
	liveFetchInFlight         bool
	lastStats                 download.SessionStats // counters for the completed download session
	lastDownloadWasWall       bool                  // the completed download targeted a wall
}

type loadingTickMsg struct{}

func loadingTickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*500, func(t time.Time) tea.Msg {
		return loadingTickMsg{}
	})
}

type updateCheckMsg struct {
	Available bool
	Version   string
}

type LiveStatusUpdateMsg struct{}

type delayedDownloadCompleteMsg struct {
	job   int
	stats download.SessionStats
}
type delayedLikeUnlikeCompleteMsg struct{}

type monitoringSelectedMsg struct{}

// liveStatusesMsg carries freshly fetched live statuses (model ID -> live).
type liveStatusesMsg struct {
	statuses map[string]bool
}

type fetchAccountInfoMsg struct {
	Success     bool
	Error       error
	AccountInfo core.AccountInfo
}

type editConfigMsg struct {
	Success bool
	Error   error
}

type editConfigFinishedMsg struct{}
type setupWizardFinishedMsg struct{}

type downloadErrorMsg struct {
	Error error
	job   int
}

type tickMsg struct{}

type downloadCompleteMsg struct {
	job   int
	stats download.SessionStats
}

// downloadStats snapshots the counters of the current download session.
func (m *MainModel) downloadStats() download.SessionStats {
	if m.downloader == nil {
		return download.SessionStats{}
	}
	return m.downloader.Stats()
}

// stopDownload cancels the running download and abandons its job, so its
// late result cannot change the screen.
func (m *MainModel) stopDownload() {
	if m.cancelDownload != nil {
		m.cancelDownload()
		m.cancelDownload = nil
	}
	m.downloadJob++
}

// newDownloadJob stops any running download and starts a new job.
func (m *MainModel) newDownloadJob() (context.Context, int) {
	m.stopDownload()
	m.lastStats = download.SessionStats{}
	m.lastDownloadWasWall = false
	if m.downloader != nil {
		m.downloader.ResetStats()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelDownload = cancel
	return ctx, m.downloadJob
}

type followedModelsModel struct {
	welcome        string
	selected       string
	followedModels []auth.FollowedModel
	cursorPos      int
}

type keyMap struct {
	Up        key.Binding
	Down      key.Binding
	Help      key.Binding
	Quit      key.Binding
	Filter    key.Binding
	SubFilter key.Binding
	Reset     key.Binding
	Back      key.Binding
	Select    key.Binding
	Refresh   key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Filter, k.SubFilter, k.Help, k.Quit, k.Refresh}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Filter},      // first column
		{k.Down, k.SubFilter}, // second column
		{k.Help, k.Back},
		{k.Reset, k.Select},
		{k.Quit, k.Refresh},
	}
}

var defaultKeyMap = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "move down"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	Filter: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter"),
	),
	SubFilter: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "subscription filter"),
	),
	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
	Reset: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "reset list"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back to menu"),
	),
	Select: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "refresh accounts"),
	),
}

func (m *MainModel) Init() tea.Cmd {
	// Add signal handling
	return tea.Batch(
		tea.EnterAltScreen,
		m.checkForUpdates(),
		func() tea.Msg {
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-c
				m.Cleanup()
				os.Exit(0)
			}()
			return nil
		},
	)
}

func NewMainModel(downloader *download.Downloader, version string, monitoringService *service.MonitoringService) *MainModel {
	postLinksInput := textinput.New()
	postLinksInput.Placeholder = "Paste link(s) here (space or comma separated)"
	postLinksInput.Focus()

	exploreInput := textinput.New()
	exploreInput.Placeholder = "tag(s), e.g. pregnant  (filter creators: pregnant @ user1,user2)"

	return &MainModel{
		version: version,
		options: []string{
			"Download a user's post",
			"Download individual post(s)",
			"Explore premium previews by tag",
			"Download purchased content",
			"Monitor a user's livestreams",
			"Like all of a user's post",
			"Unlike all of a user's post",
			"Run setup wizard",
			"Reset configuration",
			"Edit config.toml file",
			"Donate",
			"Quit",
		},
		downloadOptions:   []string{"All", "Timeline", "Select Wall", "Messages", "Stories"},
		cursorPos:         0,
		keys:              defaultKeyMap,
		help:              help.New(),
		downloader:        downloader,
		state:             MainMenuState,
		monitoredModels:   make(map[string]bool),
		monitoringService: monitoringService,
		isLoading:         false,
		loadingMessage:    "",
		loadingDots:       0,
		accountsFetched:   false,
		postLinksInput:    postLinksInput,
		exploreInput:      exploreInput,
		liveStatuses:      make(map[string]bool),
	}
}

func (m *MainModel) Reset() {
	m.cursorPos = 0
	m.selected = ""
	m.state = MainMenuState
	// Reset other fields as necessary
}
