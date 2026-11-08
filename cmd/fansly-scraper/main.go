package main

import (
	"fmt"

	"github.com/agnosto/fansly-scraper/auth"
	"github.com/agnosto/fansly-scraper/cmd"
	"github.com/agnosto/fansly-scraper/config"
	"github.com/agnosto/fansly-scraper/core"
	"github.com/agnosto/fansly-scraper/download"
	"github.com/agnosto/fansly-scraper/headers"
	"github.com/agnosto/fansly-scraper/logger"
	"github.com/agnosto/fansly-scraper/service"
	"github.com/agnosto/fansly-scraper/ui"
	"github.com/agnosto/fansly-scraper/updater"
	"github.com/agnosto/fansly-scraper/utils"

	//ksvc "github.com/kardianos/service"

	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	//"flag"

	tea "github.com/charmbracelet/bubbletea"
)

var ffmpegAvailable bool

const version = "v0.9.5"

func main() {
	flags, subcommand := cmd.ParseFlags()

	config.VerifyConfigOnStartup()

	configPath := config.GetConfigPath()
	cfg, err := config.LoadConfig(configPath)

	if err != nil && !flags.RunDiagnosis {
		// Only fatal if not running diagnosis, as diagnosis can test a broken config
		p := tea.NewProgram(ui.NewConfigWizardModel())
		if _, perr := p.Run(); perr != nil {
			log.Fatal(perr)
		}
		cfg, err = config.LoadConfig(configPath)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Initialize the logger if config was loaded
	if cfg != nil {
		if err := logger.InitLogger(cfg); err != nil {
			log.Fatal(err)
		}
	}

	if flags.RunDiagnosis {
		diagnosisSuite := cmd.NewDiagnosisSuite(flags.DiagnosisFlags, cfg)
		diagnosisSuite.Run()
		return
	}

	isCliMode := flags.Username != "" || flags.Monitor != "" || subcommand != "" || flags.PostID != ""
	isCliMode = isCliMode || flags.Explore != ""

	if flags.Version {
		fmt.Printf("Fansly Scraper version %s\n", version)
		return
	}

	switch subcommand {
	case "update":
		if err := updater.CheckForUpdate(version); err != nil {
			fmt.Printf("Error updating: %v\n", err)
			os.Exit(1)
		}
		return
	case "monitor":
		switch flags.MonitorCommand {
		case "start":
			startMonitoring()
		case "stop":
			stopMonitoring()
		default:
			fmt.Println("Usage: ./fansly-scraper monitor [start|stop]")
		}
		return
	}

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalChan
		fmt.Println("Received interrupt signal. Shutting down...")
		//stopMonitoring()
		//cleanupLockFiles()
		os.Exit(0)
	}()

	var updateAvailable bool
	var latestVersion string
	if cfg.Options.CheckUpdates {
		available, latestVer, err := updater.CheckUpdateAvailable(version)
		if err == nil && available {
			updateAvailable = available
			latestVersion = latestVer

			if isCliMode {
				fmt.Printf("Update %s available! Run 'fansly-scraper update' to update.\n", latestVer)
			}
		}
	}

	logger.Logger.Printf("Starting Fansly Scraper version %s", version)

	ffmpegAvailable = cmd.IsFFmpegAvailable()
	logger.Logger.Printf("Ffmpeg Check Returned %v", ffmpegAvailable)

	if flags.Limit > 0 {
		cfg.Options.PostLimit = flags.Limit
		logger.Logger.Printf("Overriding config: Setting Post Limit to %d", flags.Limit)
	}

	downloader, err := download.NewDownloader(cfg, ffmpegAvailable)
	if err != nil {
		logger.Logger.Fatal(err)
	}

	// Explore mode: discover premium-post previews by tag, optionally filtered
	// to specific creators with -u. Runs before the username download path since
	// here -u is a creator filter, not a download target.
	if flags.Explore != "" {
		runExploreMode(flags.Explore, flags.Username, flags.Limit, downloader)
		return
	}

	if flags.DumpChatLog && flags.Username != "" {
		// Need to load config and headers manually if not already done in main flow
		cfg, err := config.LoadConfig(config.GetConfigPath())
		if err != nil {
			logger.Logger.Printf("Failed to load config: %v", err)
			os.Exit(1)
		}
		fanslyHeaders, err := headers.NewFanslyHeaders(cfg)
		if err != nil {
			logger.Logger.Printf("Failed to create headers: %v", err)
			os.Exit(1)
		}

		// Perform login to ensure we can fetch user details
		if _, err := auth.Login(fanslyHeaders); err != nil {
			logger.Logger.Printf("Error logging in: %v", err)
			os.Exit(1)
		}

		// Get Model ID
		users := parseUsernames(flags.Username)
		for _, u := range users {
			modelID, err := core.GetModelIDFromUsername(u)
			if err != nil {
				logger.Logger.Printf("Error getting model ID for %s: %v", u, err)
				continue
			}
			if err := downloader.DumpChatLogs(context.Background(), modelID, u); err != nil {
				logger.Logger.Printf("Error dumping chat logs for %s: %v", u, err)
			}
		}
		return
	}

	// Add logic to handle the new --post flag
	if flags.PostID != "" {
		runDownloadPostMode(flags.PostID, downloader)
		return
	}

	if flags.Username != "" && flags.DownloadType == "" {
		if flags.WallID != "" {
			flags.DownloadType = "timeline"
		} else {
			flags.DownloadType = "all"
		}
	}

	if flags.Username != "" && flags.DownloadType != "" {
		users := parseUsernames(flags.Username)
		for _, u := range users {
			runCLIMode(u, flags.DownloadType, downloader, flags.WallID, flags.WallName)
		}
		return
	}

	monitoringService := service.NewMonitoringService(
		filepath.Join(config.GetConfigDir(), "monitoring_state.json"),
		logger.Logger,
	)

	if flags.Monitor != "" {
		monitors := parseUsernames(flags.Monitor)
		for _, m := range monitors {
			modelID, err := core.GetModelIDFromUsername(m)
			if err != nil {
				logger.Logger.Printf("Error getting model ID for %s: %v", m, err)
				continue
			}
			started := monitoringService.ToggleMonitoring(modelID, m)
			if started {
				fmt.Printf("Started monitoring for %s\n", m)
			} else {
				fmt.Printf("Stopped monitoring for %s\n", m)
			}
		}
		return
	}

	if subcommand == "monitor" && flags.MonitorCommand == "start" {
		startMonitoring()
		return
	}

	model := ui.NewMainModel(downloader, version, monitoringService)
	model.UpdateAvailable = updateAvailable
	model.LatestVersion = latestVersion
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Add cleanup on program exit
	defer func() {
		model.Cleanup()
	}()

	if _, err := p.Run(); err != nil {
		logger.Logger.Printf("Error: %v", err)
		os.Exit(1)
	}
}

// New function to handle downloading a single post
func runDownloadPostMode(postIdentifier string, downloader *download.Downloader) {
	fmt.Printf("Starting download for post: %s\n", postIdentifier)
	err := downloader.DownloadPostByID(context.Background(), postIdentifier)
	if err != nil {
		fmt.Printf("Error downloading post: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Post download complete.")
	fmt.Println(downloader.Stats().Summary())
}

// runExploreMode downloads premium-post previews discovered via the Explore
// feed for one or more tags, optionally limited to specific creators.
func runExploreMode(exploreTags, usernameFilter string, limit int, downloader *download.Downloader) {
	tags := parseUsernames(exploreTags)
	var users []string
	if usernameFilter != "" {
		users = parseUsernames(usernameFilter)
	}
	if len(users) > 0 {
		fmt.Printf("Exploring tag(s) %s, keeping creators: %s\n", strings.Join(tags, ", "), strings.Join(users, ", "))
	} else {
		fmt.Printf("Exploring tag(s): %s\n", strings.Join(tags, ", "))
	}
	if err := downloader.DownloadExplore(context.Background(), tags, users, limit); err != nil {
		fmt.Printf("Error during explore: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Explore download complete.")
	fmt.Println(downloader.Stats().Summary())
}

// Updated function signature to accept wallID
func runCLIMode(username string, downloadType string, downloader *download.Downloader, wallID string, wallName string) {
	cfg, err := config.LoadConfig(config.GetConfigPath())
	if err != nil {
		logger.Logger.Printf("Failed to load config for login: %v", err)
		return
	}
	fanslyHeaders, err := headers.NewFanslyHeaders(cfg)
	if err != nil {
		logger.Logger.Printf("Failed to create headers for login: %v", err)
		return
	}

	// Perform the login to populate the user ID
	if _, err := auth.Login(fanslyHeaders); err != nil {
		logger.Logger.Printf("Error logging in: %v", err)
		return
	}
	// Get model ID from username
	modelID, err := core.GetModelIDFromUsername(username)
	if err != nil {
		logger.Logger.Printf("Error getting model ID: %v", err)
		return
	}

	var modelObj auth.FollowedModel
	if downloadType == "all" {
		accountDetails, err := auth.GetAccountDetails([]string{modelID}, fanslyHeaders)
		if err != nil {
			logger.Logger.Printf("[WARN] Failed to fetch account details for profile download: %v", err)
		} else if len(accountDetails) > 0 {
			modelObj = accountDetails[0]
		}
	}

	ctx := context.Background()

	switch downloadType {
	case "all":
		// Use wallID parameter
		if err := downloader.DownloadTimeline(ctx, modelID, username, wallID, wallName); err != nil {
			logger.Logger.Printf("Error downloading timeline: %v", err)
		}
		if err := downloader.DownloadMessages(ctx, modelID, username); err != nil {
			logger.Logger.Printf("Error downloading messages: %v", err)
		}
		if err := downloader.DownloadStories(ctx, modelID, username); err != nil {
			logger.Logger.Printf("Error downloading stories: %v", err)
		}
		if modelObj.ID != "" {
			if err := downloader.DownloadProfileContent(ctx, modelObj); err != nil {
				logger.Logger.Printf("Error downloading profile content: %v", err)
			}
		}
	case "timeline":
		// Use wallID parameter
		if err := downloader.DownloadTimeline(ctx, modelID, username, wallID, wallName); err != nil {
			logger.Logger.Printf("Error downloading timeline: %v", err)
		}
	case "messages":
		if err := downloader.DownloadMessages(ctx, modelID, username); err != nil {
			logger.Logger.Printf("Error downloading messages: %v", err)
		}
	case "stories":
		if err := downloader.DownloadStories(ctx, modelID, username); err != nil {
			logger.Logger.Printf("Error downloading timeline: %v", err)
		}
	default:
		logger.Logger.Printf("Invalid download type. Use 'all', 'timeline', or 'messages'")
		os.Exit(1)
	}

	fmt.Println(downloader.Stats().Summary())
}

func parseUsernames(input string) []string {
	parts := strings.Split(input, ",")
	var res []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			res = append(res, p)
		}
	}
	return res
}

func startMonitoring() {
	pidFile := filepath.Join(config.GetConfigDir(), "monitor.pid")

	// Check existing process
	if data, err := os.ReadFile(pidFile); err == nil {
		pid, err := strconv.Atoi(string(data))
		if err == nil && utils.IsProcessRunning(pid) {
			fmt.Println("Monitoring process is already running.")
			return
		}
		// Clean up stale PID file
		os.Remove(pidFile)
	}

	pid := os.Getpid()
	pidStr := strconv.Itoa(pid)
	if err := os.WriteFile(pidFile, []byte(pidStr), 0644); err != nil {
		fmt.Printf("Error writing PID file: %v\n", err)
		return
	}

	// Ensure cleanup on exit
	defer func() {
		cleanupLockFiles()
		os.Remove(pidFile)
	}()

	fmt.Printf("Started monitoring process with PID %d\n", pid)

	// Load config and handle potential error
	cfg, err := config.LoadConfig(config.GetConfigPath())
	if err != nil {
		log.Fatal(err)
	}

	// Initialize logger with the loaded config
	if err := logger.InitLogger(cfg); err != nil {
		log.Fatal(err)
	}

	monitoringService := service.NewMonitoringService(
		filepath.Join(config.GetConfigDir(), "monitoring_state.json"),
		logger.Logger,
	)

	monitoringService.StartMonitoring()
	go monitoringService.Run()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	<-signalChan
	fmt.Println("Received interrupt signal. Shutting down monitoring...")
	monitoringService.Shutdown()
}

func stopMonitoring() {
	pidFile := filepath.Join(config.GetConfigDir(), "monitor.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Println("No monitoring process is running.")
		return
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		fmt.Printf("Error reading PID: %v\n", err)
		return
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("Error finding process: %v\n", err)
		return
	}

	if err := process.Signal(os.Interrupt); err != nil {
		fmt.Printf("Error stopping process: %v\n", err)
		return
	}

	if err := os.Remove(pidFile); err != nil {
		fmt.Printf("Error removing PID file: %v\n", err)
		return
	}

	// I don't even think this function would really be used
	// startMonitoring isn't a background process
	cleanupLockFiles()

	fmt.Println("Monitoring process stopped.")
}

func cleanupLockFiles() {
	recordingsPath := filepath.Join(config.GetConfigDir(), "active_recordings")
	files, err := os.ReadDir(recordingsPath)
	if err != nil {
		fmt.Printf("Error reading recordings directory: %v\n", err)
		return
	}

	for _, file := range files {
		if filepath.Ext(file.Name()) == ".lock" {
			lockFile := filepath.Join(recordingsPath, file.Name())
			if err := os.Remove(lockFile); err != nil {
				fmt.Printf("Error removing lock file %s: %v\n", lockFile, err)
			} else {
				fmt.Printf("Removed lock file: %s\n", lockFile)
			}
		}
	}
}
