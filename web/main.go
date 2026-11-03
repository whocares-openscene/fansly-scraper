package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/a-h/templ"
	"github.com/agnosto/fansly-scraper/config"
	"github.com/agnosto/fansly-scraper/core"
	"github.com/agnosto/fansly-scraper/download"
	"github.com/agnosto/fansly-scraper/logger"
	"github.com/agnosto/fansly-scraper/web/templates"
	"github.com/gorilla/websocket"
)

var (
	upgrader   = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	jobManager = NewJobManager()
	server     *http.Server
)

type Job struct {
	ID        string
	ModelName string
	Cancel    context.CancelFunc
	Progress  chan templates.JobProgress
	Done      chan error
}

type JobManager struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewJobManager() *JobManager {
	return &JobManager{jobs: make(map[string]*Job)}
}

func (jm *JobManager) Create(modelName string) *Job {
	_, cancel := context.WithCancel(context.Background())
	job := &Job{
		ID:        fmt.Sprintf("job-%d", time.Now().UnixNano()),
		ModelName: modelName,
		Cancel:    cancel,
		Progress:  make(chan templates.JobProgress, 100),
		Done:      make(chan error, 1),
	}
	jm.mu.Lock()
	jm.jobs[job.ID] = job
	jm.mu.Unlock()
	return job
}

func (jm *JobManager) Get(id string) (*Job, bool) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	job, ok := jm.jobs[id]
	return job, ok
}

func (jm *JobManager) Delete(id string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	delete(jm.jobs, id)
}

func main() {
	cfg, err := config.LoadConfig(config.GetConfigPath())
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if err := logger.InitLogger(cfg); err != nil {
		log.Fatalf("Failed to init logger: %v", err)
	}

	downloader, err := download.NewDownloader(cfg, IsFFmpegAvailable())
	if err != nil {
		log.Fatalf("Failed to create downloader: %v", err)
	}
	defer downloader.Close()

	mux := http.NewServeMux()

	// Static files (for future use)
	// mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	// WebSocket for progress
	mux.HandleFunc("/ws/progress/", handleProgressWS)

	// Main pages
	mux.HandleFunc("/", handleDashboard(downloader))
	mux.HandleFunc("/models", handleModels(downloader))
	mux.HandleFunc("/models/refresh", handleRefreshModels(downloader))

	// Model actions
	mux.HandleFunc("/models/", handleModelActions(downloader))

	// Download actions
	mux.HandleFunc("/download/", handleDownloadActions(downloader))

	// Explore
	mux.HandleFunc("/explore", handleExplore(downloader))
	mux.HandleFunc("/explore/start", handleExploreStart(downloader))

	// Job management
	mux.HandleFunc("/jobs/", handleJobActions())

	// Shutdown endpoint
	mux.HandleFunc("/shutdown", handleShutdown)

	log.Println("Starting web server on :8080")
	server = &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func handleDashboard(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		models, _ := fetchModels(downloader)
		component := templates.Dashboard(models, nil, "ws://"+r.Host)
		templ.Handler(component).ServeHTTP(w, r)
	}
}

func handleModels(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := fetchModels(downloader)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Return partial for HTMX
		component := templates.Dashboard(models, nil, "ws://"+r.Host)
		templ.Handler(component).ServeHTTP(w, r)
	}
}

func handleRefreshModels(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
			return
		}
		// Clear any cached models by forcing refetch
		models, err := fetchModels(downloader)
		if err != nil {
			component := templates.Dashboard(nil, &templates.Flash{Type: "error", Message: "Failed to refresh: " + err.Error()}, "ws://"+r.Host)
			templ.Handler(component).ServeHTTP(w, r)
			return
		}
		component := templates.Dashboard(models, &templates.Flash{Type: "success", Message: "Models refreshed successfully"}, "ws://"+r.Host)
		templ.Handler(component).ServeHTTP(w, r)
	}
}

func handleModelActions(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/models/")
		parts := strings.Split(path, "/")
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}
		modelID := parts[0]

		// Get model details
		model, err := getModelByID(downloader, modelID)
		if err != nil {
			http.Error(w, "Model not found", 404)
			return
		}

		walls, _ := getWallsForModel(downloader, modelID)

		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			// Show download modal
			component := templates.DownloadModal(model.Username, model.DisplayName, model.ID, walls)
			templ.Handler(component).ServeHTTP(w, r)

		case len(parts) == 2 && parts[1] == "walls" && r.Method == http.MethodGet:
			// Show walls modal
			component := templates.WallModal(model.Username, model.ID, walls)
			templ.Handler(component).ServeHTTP(w, r)

		case len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet:
			// Show download modal (with optional wall pre-selection)
			wallID := r.URL.Query().Get("wall_id")
			_ = r.URL.Query().Get("wall_name")
			if wallID != "" {
				// Pre-select this wall in the form
				for i := range walls {
					if walls[i].ID == wallID {
						walls = append([]templates.Wall{walls[i]}, walls[:i]...)
						walls = append(walls, walls[i+1:]...)
						break
					}
				}
			}
			component := templates.DownloadModal(model.Username, model.DisplayName, model.ID, walls)
			templ.Handler(component).ServeHTTP(w, r)

		case len(parts) == 3 && parts[1] == "download" && parts[2] == "start" && r.Method == http.MethodPost:
			// Start download
			handleDownloadStart(w, r, downloader, model, walls)
		}
	}
}

func handleDownloadActions(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/download/")
		
		switch {
		case path == "individual" && r.Method == http.MethodGet:
			component := templates.IndividualPostModal()
			templ.Handler(component).ServeHTTP(w, r)
			
		case path == "post/start" && r.Method == http.MethodPost:
			handleIndividualPostStart(w, r, downloader)
		}
	}
}

func handleExplore(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		component := templates.ExploreModal()
		templ.Handler(component).ServeHTTP(w, r)
	}
}

func handleExploreStart(downloader *download.Downloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		tags := r.FormValue("tags")
		_ = r.FormValue("creators")
		_ = parseInt(r.FormValue("limit"), 0)

		job := jobManager.Create("Explore: " + tags)
		go func() {
			defer jobManager.Delete(job.ID)
			_ = context.Background()

			// TODO: Implement explore download
			job.Progress <- templates.JobProgress{Type: "status", Message: "Explore download not yet implemented"}
			job.Done <- fmt.Errorf("not implemented")
		}()

		wsURL := fmt.Sprintf("ws://%s/ws/progress/%s", r.Host, job.ID)
		component := templates.ProgressModal(job.ID, job.ModelName, wsURL)
		templ.Handler(component).ServeHTTP(w, r)
	}
}

func handleDownloadStart(w http.ResponseWriter, r *http.Request, baseDownloader *download.Downloader, model templates.Model, walls []templates.Wall) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	downloadType := r.FormValue("download_type")
	wallID := r.FormValue("wall_id")
	wallName := r.FormValue("wall_name")
	limit := parseInt(r.FormValue("limit"), 0)

	// Parse new format: "timeline_wall_{wallID}" for wall-specific timeline downloads
	if strings.HasPrefix(downloadType, "timeline_wall_") {
		wallID = strings.TrimPrefix(downloadType, "timeline_wall_")
		downloadType = "timeline"
		// Find wall name for display
		for _, w := range walls {
			if w.ID == wallID {
				wallName = w.Name
				break
			}
		}
	}

	cfg, err := config.LoadConfig(config.GetConfigPath())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if limit > 0 {
		cfg.Options.PostLimit = limit
		config.SaveConfig(cfg)
	}

	job := jobManager.Create(model.DisplayName)

	// Create a new downloader for this job with progress callback
	jobDownloader, err := download.NewDownloader(cfg, IsFFmpegAvailable(), download.WithProgressCallback(func(update download.ProgressUpdate) {
		job.Progress <- templates.JobProgress{
			Type:    update.Type,
			Percent: update.Percent,
			Message: update.Message,
		}
	}))
	if err != nil {
		log.Printf("Failed to create job downloader: %v", err)
		job.Done <- err
		jobManager.Delete(job.ID)
		component := templates.ProgressModal(job.ID, job.ModelName, "ws://"+r.Host)
		templ.Handler(component).ServeHTTP(w, r)
		return
	}

	log.Printf("Starting download for model %s (ID: %s), job: %s, type: %s, wall: %s", model.Username, model.ID, job.ID, downloadType, wallID)

	go func() {
		defer jobManager.Delete(job.ID)
		defer jobDownloader.Close()
		ctx := context.Background()

		var err error
		switch downloadType {
		case "all":
			err = jobDownloader.DownloadTimeline(ctx, model.ID, model.Username, wallID, wallName)
			if err == nil {
				err = jobDownloader.DownloadMessages(ctx, model.ID, model.Username)
			}
			if err == nil {
				err = jobDownloader.DownloadStories(ctx, model.ID, model.Username)
			}
		case "timeline":
			err = jobDownloader.DownloadTimeline(ctx, model.ID, model.Username, wallID, wallName)
		case "messages":
			err = jobDownloader.DownloadMessages(ctx, model.ID, model.Username)
		case "stories":
			err = jobDownloader.DownloadStories(ctx, model.ID, model.Username)
		}

		log.Printf("Download job %s completed with error: %v", job.ID, err)
		job.Done <- err
	}()

	wsURL := fmt.Sprintf("ws://%s/ws/progress/%s", r.Host, job.ID)
	log.Printf("Rendering ProgressModal for job %s with wsURL: %s", job.ID, wsURL)
	component := templates.ProgressModal(job.ID, job.ModelName, wsURL)
	log.Printf("Component created, rendering to response writer")
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("Failed to render ProgressModal: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("ProgressModal rendered successfully")
}

func handleIndividualPostStart(w http.ResponseWriter, r *http.Request, baseDownloader *download.Downloader) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	postIDs := r.FormValue("post_ids")
	ids := strings.Split(postIDs, "\n")

	cfg, err := config.LoadConfig(config.GetConfigPath())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	job := jobManager.Create("Individual Posts")

	// Create a new downloader for this job with progress callback
	jobDownloader, err := download.NewDownloader(cfg, IsFFmpegAvailable(), download.WithProgressCallback(func(update download.ProgressUpdate) {
		job.Progress <- templates.JobProgress{
			Type:    update.Type,
			Percent: update.Percent,
			Message: update.Message,
		}
	}))
	if err != nil {
		job.Done <- err
		jobManager.Delete(job.ID)
		component := templates.ProgressModal(job.ID, job.ModelName, "ws://"+r.Host)
		templ.Handler(component).ServeHTTP(w, r)
		return
	}

	go func() {
		defer jobManager.Delete(job.ID)
		defer jobDownloader.Close()
		ctx := context.Background()

		for i, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			// Extract ID from URL if needed
			if strings.Contains(id, "fansly.com") {
				parts := strings.Split(id, "/")
				id = parts[len(parts)-1]
			}

			job.Progress <- templates.JobProgress{Type: "post_start", Message: fmt.Sprintf("Downloading post %s (%d/%d)", id, i+1, len(ids)), Current: i, Total: len(ids)}
			err := jobDownloader.DownloadPostByID(ctx, id)
			if err != nil {
				job.Progress <- templates.JobProgress{Type: "log", Message: "Error: " + err.Error()}
			} else {
				job.Progress <- templates.JobProgress{Type: "log", Message: "Success: " + id}
			}
		}
		job.Done <- nil
	}()

	wsURL := fmt.Sprintf("ws://%s/ws/progress/%s", r.Host, job.ID)
	component := templates.ProgressModal(job.ID, job.ModelName, wsURL)
	templ.Handler(component).ServeHTTP(w, r)
}

func handleJobActions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/jobs/")
		parts := strings.Split(path, "/")
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}
		jobID := parts[0]
		
		job, ok := jobManager.Get(jobID)
		if !ok {
			http.Error(w, "Job not found", 404)
			return
		}
		
		if len(parts) == 1 && parts[0] == "cancel" && r.Method == http.MethodPost {
			job.Cancel()
			wsURL := fmt.Sprintf("ws://%s/ws/progress/%s", r.Host, job.ID)
			component := templates.ProgressModal(job.ID, job.ModelName, wsURL)
			templ.Handler(component).ServeHTTP(w, r)
		}
	}
}

func handleProgressWS(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/ws/progress/")
	jobID := path
	
	job, ok := jobManager.Get(jobID)
	if !ok {
		http.Error(w, "Job not found", 404)
		return
	}
	
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	
	// Send initial status
	conn.WriteJSON(map[string]any{"type": "status", "message": "Connecting..."})
	
	// Read from job progress channel and send to WebSocket
	done := make(chan struct{})
	go func() {
		for {
			select {
			case progress := <-job.Progress:
				if err := conn.WriteJSON(progress); err != nil {
					return
				}
			case err := <-job.Done:
				if err != nil {
					conn.WriteJSON(map[string]any{"type": "log", "message": "Error: " + err.Error()})
				} else {
					conn.WriteJSON(map[string]any{"type": "status", "message": "Completed successfully"})
					conn.WriteJSON(map[string]any{"type": "progress", "percent": 100})
				}
				close(done)
				return
			case <-r.Context().Done():
				return
			}
		}
	}()
	
	<-done
}

func fetchModels(downloader *download.Downloader) ([]templates.Model, error) {
	accountInfo, err := core.FetchAccountInfo(config.GetConfigPath())
		if err != nil {
			return nil, err
		}

		models := make([]templates.Model, len(accountInfo.FollowedModels))
	for i, m := range accountInfo.FollowedModels {
		walls := make([]templates.Wall, len(m.Walls))
		for j, w := range m.Walls {
			walls[j] = templates.Wall{ID: w.ID, Name: w.Name, Description: w.Description, Pos: w.Pos}
		}
		models[i] = templates.Model{ID: m.ID, Username: m.Username, DisplayName: m.DisplayName, Walls: walls}
	}
	return models, nil
}

func getModelByID(downloader *download.Downloader, modelID string) (templates.Model, error) {
	models, err := fetchModels(downloader)
	if err != nil {
		return templates.Model{}, err
	}
	for _, m := range models {
		if m.ID == modelID {
			return m, nil
		}
	}
	return templates.Model{}, fmt.Errorf("model not found")
}

func getWallsForModel(downloader *download.Downloader, modelID string) ([]templates.Wall, error) {
	model, err := getModelByID(downloader, modelID)
	if err != nil {
		return nil, err
	}
	return model.Walls, nil
}

func parseInt(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

func IsFFmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Println("Shutdown requested via API")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "shutting down"}`))

	// Shutdown in a goroutine to allow response to be sent first
	go func() {
		time.Sleep(500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()
}