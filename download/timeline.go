package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"

	"github.com/agnosto/fansly-scraper/config"
	"github.com/agnosto/fansly-scraper/db"
	"github.com/agnosto/fansly-scraper/logger"
	"github.com/agnosto/fansly-scraper/posts"
	"github.com/agnosto/fansly-scraper/utils"

	"github.com/agnosto/fansly-scraper/headers"
)

var (
	// Regex to match characters not safe for filenames
	invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
)

// sanitizeForFilename replaces invalid filesystem characters with underscores
func sanitizeForFilename(name string) string {
	// Replace invalid characters
	name = invalidFilenameChars.ReplaceAllString(name, "_")
	// Remove any non-printable characters and emojis (non-ASCII)
	name = strings.Map(func(r rune) rune {
		if r >= 32 && r <= 126 {
			return r
		}
		return '_'
	}, name)
	// Collapse multiple underscores
	name = regexp.MustCompile(`_+`).ReplaceAllString(name, "_")
	// Trim spaces, dots, and underscores from ends
	name = strings.Trim(name, " ._")
	// Limit length
	if len(name) > 100 {
		name = name[:100]
	}
	if name == "" {
		name = "wall"
	}
	return name
}

var (
	resolveMu       sync.Mutex
	activeDownloads sync.Map
)

type Downloader struct {
	database        *db.Database
	saveLocation    string
	M3U8Download    bool
	headers         *headers.FanslyHeaders
	limiter         *rate.Limiter
	progressBar     *progressbar.ProgressBar
	ffmpegAvailable bool
	cfg             *config.Config
	m3u8Semaphore   *semaphore.Weighted
	progressFn      func(ProgressUpdate)
}

type ProgressUpdate struct {
	Type    string // "progress", "status", "log", "post_start", "post_done"
	Percent int
	Message string
	Current int
	Total   int
}

func NewDownloader(cfg *config.Config, ffmpegAvailable bool, opts ...func(*Downloader)) (*Downloader, error) {
	// Initialize database
	database, err := db.NewDatabase(cfg.Options.SaveLocation)
	if err != nil {
		logger.Logger.Printf("Error initializing database: %v", err)
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	fanslyHeaders, err := headers.NewFanslyHeaders(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Fansly headers: %v", err)
	}

	limiter := rate.NewLimiter(rate.Every(2*time.Second), 3)
	bar := progressbar.NewOptions(-1,
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(15),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionSetDescription("[cyan]Downloading[reset]"),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]=[reset]",
			SaucerHead:    "[green]>[reset]",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	d := &Downloader{
		database:        database,
		saveLocation:    cfg.Options.SaveLocation,
		M3U8Download:    cfg.Options.M3U8Download,
		headers:         fanslyHeaders,
		limiter:         limiter,
		progressBar:     bar,
		ffmpegAvailable: ffmpegAvailable,
		cfg:             cfg,
		m3u8Semaphore:   semaphore.NewWeighted(2),
	}

	for _, opt := range opts {
		opt(d)
	}

	return d, nil
}

func WithProgressCallback(fn func(ProgressUpdate)) func(*Downloader) {
	return func(d *Downloader) {
		d.progressFn = fn
	}
}

func (d *Downloader) reportProgress(update ProgressUpdate) {
	if d.progressFn != nil {
		d.progressFn(update)
	}
}

func (d *Downloader) DownloadTimeline(ctx context.Context, modelId, modelName, wallID, wallName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	timelinePosts, err := posts.GetAllTimelinePosts(ctx, modelId, wallID, d.headers, d.cfg.Options.PostLimit)
	if err != nil {
		logger.Logger.Printf("[ERROR] [%s] Failed to get timeline posts: %v", modelName, err)
		return err
	}

	d.reportProgress(ProgressUpdate{Type: "status", Message: fmt.Sprintf("Found %d posts", len(timelinePosts)), Current: 0, Total: len(timelinePosts)})

	folderName := "timeline"
	if wallID != "" {
		if wallName == "" {
			wallName = "wall"
		}
		// Sanitize wall name for filesystem
		safeWallName := sanitizeForFilename(wallName)
		folderName = fmt.Sprintf("%s_%s", safeWallName, wallID)
	}

	baseDir := filepath.Join(d.saveLocation, strings.ToLower(modelName), folderName)
	for _, subDir := range []string{"images", "videos", "audios"} {
		if err = os.MkdirAll(filepath.Join(baseDir, subDir), os.ModePerm); err != nil {
			return err
		}
	}

	d.progressBar = progressbar.NewOptions(len(timelinePosts),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionUseANSICodes(true),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetDescription(fmt.Sprintf("[cyan]Processing %s Timeline[reset]", modelName)),
		progressbar.OptionSetWidth(30),
		progressbar.OptionThrottle(30*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]=[reset]",
			SaucerHead:    "[green]>[reset]",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	tickerDone := make(chan bool)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-tickerDone:
				return
			case <-ticker.C:
				if d.progressBar != nil {
					d.progressBar.Add(0)
				}
			}
		}
	}()

	// Pre-fetch media details for all non-skipped posts in batched calls:
	// the per-post endpoint is rate limited, so fetching one post at a time
	// serializes the whole timeline regardless of download concurrency.
	skipFiles := make(map[string]bool, len(timelinePosts))
	var postsToFetch []string
	for _, post := range timelinePosts {
		if d.cfg.Options.SkipDownloadedPosts && d.database.PostExists(post.ID) {
			skipFiles[post.ID] = true
		} else {
			postsToFetch = append(postsToFetch, post.ID)
		}
	}

	mediaByPost, err := posts.GetPostsMediaBatch(ctx, postsToFetch, d.headers)
	if err != nil {
		logger.Logger.Printf("[WARN] [%s] Some post batches failed to fetch: %v", modelName, err)
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 10)

	completedPosts := 0
	var completedMu sync.Mutex

	for _, post := range timelinePosts {
		wg.Add(1)
		go func(post posts.Post) {
			defer wg.Done()

			downloadFailed := false
			if !skipFiles[post.ID] {
				func() {
					semaphore <- struct{}{}
					defer func() { <-semaphore }()

					accountMediaItems, ok := mediaByPost[post.ID]
					if !ok {
						logger.Logger.Printf("[ERROR] [%s] Failed to fetch media for post %s", modelName, post.ID)
						downloadFailed = true
						return
					}

					d.reportProgress(ProgressUpdate{Type: "post_start", Message: fmt.Sprintf("Downloading post %s (%d items)", post.ID, len(accountMediaItems)), Current: completedPosts, Total: len(timelinePosts)})

					for i, accountMedia := range accountMediaItems {
						err := d.DownloadMediaItem(ctx, accountMedia, baseDir, modelName, post, i)
						if err != nil {
							logger.Logger.Printf("[ERROR] [%s] Failed to download media item %s: %v", modelName, accountMedia.ID, err)
							downloadFailed = true
							continue
						}
					}
				}()
			} else {
				logger.Logger.Printf("[INFO] [%s] Skipping files for post %s, but updating metadata", modelName, post.ID)
			}

			// Only mark the post processed when nothing failed, so failed
			// posts are retried on the next run instead of skipped forever.
			if downloadFailed {
				logger.Logger.Printf("[WARN] [%s] Post %s had failures, leaving unmarked for retry", modelName, post.ID)
			} else {
				err := d.database.MarkPostAsProcessed(post.ID, modelName, post.Content, post.CreatedAt)
				if err != nil {
					logger.Logger.Printf("[ERROR] [%s] Failed to save post metadata %s: %v", modelName, post.ID, err)
				}
			}

			completedMu.Lock()
			completedPosts++
			percent := 0
			if len(timelinePosts) > 0 {
				percent = (completedPosts * 100) / len(timelinePosts)
			}
			d.reportProgress(ProgressUpdate{Type: "progress", Percent: percent, Message: fmt.Sprintf("Completed %d/%d posts", completedPosts, len(timelinePosts)), Current: completedPosts, Total: len(timelinePosts)})
			completedMu.Unlock()

			d.progressBar.Add(1)
		}(post)
	}
	wg.Wait()

	tickerDone <- true
	close(tickerDone)

	d.progressBar.Finish()
	fmt.Println()
	d.reportProgress(ProgressUpdate{Type: "progress", Percent: 100, Message: "Timeline download complete", Current: len(timelinePosts), Total: len(timelinePosts)})
	return ctx.Err()
}

func (d *Downloader) DownloadMediaItem(ctx context.Context, accountMedia posts.AccountMedia, baseDir, modelName string, contentSource any, index int, isDiagnosis ...bool) error {
	diagMode := len(isDiagnosis) > 0 && isDiagnosis[0]
	return d.downloadMediaItem(ctx, accountMedia, baseDir, modelName, contentSource, index, false, diagMode)
}

// downloadMediaItem is the shared implementation behind DownloadMediaItem.
// forcePreview downloads the preview even when SkipPreviews is set in config,
// which the Explore feature relies on since previews are the whole point there.
func (d *Downloader) downloadMediaItem(ctx context.Context, accountMedia posts.AccountMedia, baseDir, modelName string, contentSource any, index int, forcePreview, diagMode bool) error {
	if accountMedia.Media.HasLocations() {
		// Use `contentSource` which can be a post, message, or anything else
		err := d.downloadSingleItem(ctx, accountMedia, accountMedia.Media, baseDir, modelName, false, contentSource, index, diagMode)
		if err != nil {
			logger.Logger.Printf("[ERROR] [%s] Failed to download main media item %s: %v", modelName, accountMedia.ID, err)
			return fmt.Errorf("error downloading main media: %v", err)
		}
	}

	if (forcePreview || !d.cfg.Options.SkipPreviews) && accountMedia.Preview != nil && accountMedia.Preview.HasLocations() {
		// Use `contentSource` here as well
		err := d.downloadSingleItem(ctx, accountMedia, *accountMedia.Preview, baseDir, modelName, true, contentSource, index, diagMode)
		if err != nil {
			logger.Logger.Printf("[ERROR] [%s] Failed to download preview item for media item %s : %v", modelName, accountMedia.ID, err)
			return fmt.Errorf("error downloading preview: %v", err)
		}
	}

	if !accountMedia.HasLocations() {
		d.progressBar.Describe(fmt.Sprintf("[yellow]No valid media or preview locations[reset] for item %s", accountMedia.ID))
	}

	return nil
}

func (d *Downloader) resolveUniqueFilePath(dir, filename, ext string) string {
	resolveMu.Lock()
	defer resolveMu.Unlock()

	full := filepath.Join(dir, filename)

	// If it doesn't exist in DB and doesn't exist on disk, it's safe.
	if !d.database.FileExists(full) {
		if _, err := os.Stat(full); os.IsNotExist(err) {
			// Mark active before creating the 0-byte placeholder so a
			// concurrent goroutine can't adopt the empty file into the DB.
			activeDownloads.Store(full, true)
			f, _ := os.OpenFile(full, os.O_RDONLY|os.O_CREATE, 0666)
			if f != nil {
				f.Close()
			}
			return filename
		}
	}

	stem := strings.TrimSuffix(filename, ext)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, n, ext)
		fullCandidate := filepath.Join(dir, candidate)

		if !d.database.FileExists(fullCandidate) {
			if _, err := os.Stat(fullCandidate); os.IsNotExist(err) {
				activeDownloads.Store(fullCandidate, true)
				f, _ := os.OpenFile(fullCandidate, os.O_RDONLY|os.O_CREATE, 0666)
				if f != nil {
					f.Close()
				}
				return candidate
			}
		}
		if n > 10000 {
			// Safety valve
			return fmt.Sprintf("%s_%s%s", stem, filepath.Base(dir), ext)
		}
	}
}

func (d *Downloader) generateFilename(bestMedia posts.MediaItem, modelName string, contentSource any, index int, isPreview bool, ext string) string {
	suffix := ""
	if isPreview {
		suffix = "_preview"
	}

	var sourceID string
	var date int64
	var textContent string
	var shouldUseIndex bool

	// Extract data based on the type of content (Post, Message, Story, etc.)
	if contentSource != nil {
		switch v := contentSource.(type) {
		case posts.Post:
			textContent = v.Content
			date = v.CreatedAt

			// Check if this is the "Purchases" album
			if textContent == "Purchases" {
				// Purchases: Do NOT use index (unstable), use MediaID as source
				shouldUseIndex = false
				sourceID = bestMedia.ID
			} else {
				// Timeline: Use PostID and Index
				shouldUseIndex = true
				sourceID = v.ID
			}
		case posts.Message:
			sourceID = v.ID
			textContent = v.Content
			date = v.CreatedAt
			shouldUseIndex = true // Messages often contain bundles in specific order
		case posts.Story:
			sourceID = v.ID
			date = v.CreatedAt
			shouldUseIndex = false // Stories are usually 1:1, index not required
		case posts.PostInfo: // Handle cases where PostInfo is passed directly
			sourceID = v.ID
			textContent = v.Content
			date = v.CreatedAt
			shouldUseIndex = true
		default:
			// Fallback for Purchases, Profile, or unknown types
			// We use the MediaID as the SourceID to ensure uniqueness without an unstable index
			sourceID = bestMedia.ID
			shouldUseIndex = false
		}
	} else {
		// Fallback if no source is provided
		sourceID = bestMedia.ID
		shouldUseIndex = false
	}

	// 2. Mode A: Default ID-Based Naming (Config: use_content_as_filename = false)
	if !d.cfg.Options.UseContentAsFilename {
		if shouldUseIndex {
			// Format: PostID_Index_MediaID (e.g., 84458..._01_84458... .mp4)
			// We use %02d to pad the index (01, 02, 10) for correct filesystem sorting
			return fmt.Sprintf("%s_%02d_%s%s%s", sourceID, index+1, bestMedia.ID, suffix, ext)
		}
		// Format: SourceID_MediaID (e.g. StoryID_MediaID)
		// If sourceID was set to MediaID in fallback, this effectively becomes MediaID_MediaID,
		// so we check to avoid redundancy
		if sourceID == bestMedia.ID {
			return fmt.Sprintf("%s%s%s", bestMedia.ID, suffix, ext)
		}
		return fmt.Sprintf("%s_%s%s%s", sourceID, bestMedia.ID, suffix, ext)
	}

	// 3. Mode B: Content-Based Naming (Config: use_content_as_filename = true)

	// Define Date Format
	dateFormat := d.cfg.Options.DateFormat
	if dateFormat == "" {
		dateFormat = "20060102"
	}
	var dateStr string
	if date > 0 {
		dateStr = parseUnixTimestamp(date).Format(dateFormat)
	} else {
		dateStr = time.Now().Format(dateFormat)
	}

	// Sanitize and Limit Content Text
	cleanContent := strings.ReplaceAll(textContent, "\n", " ")
	if cleanContent == "Purchases" || strings.TrimSpace(config.SanitizeFilename(cleanContent)) == "empty_content" || strings.TrimSpace(config.SanitizeFilename(cleanContent)) == "" {
		cleanContent = ""
	}
	runes := []rune(cleanContent)
	charLimit := d.cfg.Options.ContentFilenameLength
	if charLimit <= 0 {
		charLimit = 50
	}
	if len(runes) > charLimit {
		runes = runes[:charLimit]
	}
	cleanContent = string(runes)

	// Get Template
	template := d.cfg.Options.ContentFilenameTemplate
	if template == "" {
		template = "{date}-{content}_{index}"
	}

	// If template has {content} but there is no caption text, clean up resulting separators or fallback cleanly
	if cleanContent == "" && strings.Contains(template, "{content}") {
		template = strings.ReplaceAll(template, "-{content}", "")
		template = strings.ReplaceAll(template, "_{content}", "")
		template = strings.ReplaceAll(template, "{content}_", "")
		template = strings.ReplaceAll(template, "{content}-", "")
		template = strings.ReplaceAll(template, "{content}", "")
		if !strings.Contains(template, "{postId}") && !strings.Contains(template, "{mediaId}") {
			template += "_{postId}"
		}
		if strings.TrimSpace(template) == "" {
			template = "{date}_{model_name}_{mediaId}_{index}"
		}
	}

	// Replace Variables
	r := strings.NewReplacer(
		"{date}", dateStr,
		"{content}", cleanContent,
		"{index}", fmt.Sprintf("%d", index+1),
		"{postId}", sourceID,
		"{mediaId}", bestMedia.ID,
		"{model_name}", modelName,
	)

	baseName := r.Replace(template)
	return config.SanitizeFilename(baseName) + suffix + ext
}

func parseUnixTimestamp(ts int64) time.Time {
	return utils.ParseUnixTimestamp(ts)
}

func extractTimestamp(contentSource any, accountMedia posts.AccountMedia, item posts.MediaItem) time.Time {
	var ts int64

	if contentSource != nil {
		switch v := contentSource.(type) {
		case posts.Post:
			ts = v.CreatedAt
		case posts.Message:
			ts = v.CreatedAt
		case posts.Story:
			ts = v.CreatedAt
		case posts.PostInfo:
			ts = v.CreatedAt
		}
	}

	if ts <= 0 {
		ts = accountMedia.CreatedAt
	}

	if ts <= 0 {
		ts = item.CreatedAt
	}

	return parseUnixTimestamp(ts)
}

func applyFileTimestamp(filePath string, t time.Time) {
	if t.IsZero() {
		return
	}
	if err := os.Chtimes(filePath, t, t); err != nil {
		logger.Logger.Printf("[WARN] Failed to set file timestamp for %s: %v", filePath, err)
	}
}

func (d *Downloader) downloadSingleItem(ctx context.Context, accountMedia posts.AccountMedia, item posts.MediaItem, baseDir, modelName string, isPreview bool, contentSource any, index int, isDiagnosis bool) error {
	getMediaType := func(mimetype string) string {
		switch {
		case strings.HasPrefix(mimetype, "image/"):
			return "image"
		case strings.HasPrefix(mimetype, "video/") || mimetype == "application/vnd.apple.mpegurl":
			return "video"
		case strings.HasPrefix(mimetype, "audio/"):
			return "audio"
		default:
			return "unknown"
		}
	}

	var sourceID string
	if contentSource != nil {
		switch v := contentSource.(type) {
		case posts.Post:
			sourceID = v.ID
		case posts.Message:
			sourceID = v.ID
		case posts.Story:
			sourceID = v.ID
		case posts.PostInfo:
			sourceID = v.ID
		}
	}

	mainType := getMediaType(item.Mimetype)
	allowHLS := d.M3U8Download && d.ffmpegAvailable
	bestMedia := item
	found := len(item.Locations) > 0 && (item.Mimetype != "application/vnd.apple.mpegurl" || allowHLS)
	for _, variant := range item.Variants {
		if len(variant.Locations) == 0 || getMediaType(variant.Mimetype) != mainType {
			continue
		}
		if variant.Mimetype == "application/vnd.apple.mpegurl" && !allowHLS {
			continue
		}
		if !found || variant.Height > bestMedia.Height {
			bestMedia = posts.MediaItem{
				ID:        variant.ID,
				Type:      variant.Type,
				Height:    variant.Height,
				Mimetype:  variant.Mimetype,
				Metadata:  variant.Metadata,
				CreatedAt: variant.CreatedAt,
				UpdatedAt: variant.UpdatedAt,
				Locations: variant.Locations,
			}
			found = true
		}
	}
	if !found {
		d.progressBar.Describe(fmt.Sprintf("[red]No suitable media found[reset] for item %s", item.ID))
		return nil
	}

	var fileType, subDir string
	switch {
	case strings.HasPrefix(item.Mimetype, "image/"):
		subDir, fileType = "images", "image"
	case strings.HasPrefix(item.Mimetype, "video/") || item.Mimetype == "application/vnd.apple.mpegurl":
		subDir, fileType = "videos", "video"
		if item.Mimetype == "application/vnd.apple.mpegurl" {
			item.Mimetype = "video/mp4"
		}
	case strings.HasPrefix(item.Mimetype, "audio/"):
		subDir, fileType = "audios", "audio"
		if item.Mimetype == "audio/mp4" {
			item.Mimetype = "audio/mp3"
		}
	default:
		return fmt.Errorf("unknown media type: %s", item.Mimetype)
	}

	mediaTypeFilter := d.cfg.Options.DownloadMediaType
	normalizedFilter := strings.TrimSuffix(mediaTypeFilter, "s")
	if normalizedFilter != "all" && normalizedFilter != fileType {
		d.progressBar.Describe(fmt.Sprintf("[yellow]Skipping[reset] %s due to media type filter (%s)", item.ID, mediaTypeFilter))
		return nil // Not an error, just skipping
	}

	if d.cfg.Options.SkipLongVideos && fileType == "video" {
		maxDur := d.cfg.Options.MaxVideoDurationSeconds
		if maxDur <= 0 {
			maxDur = 2700 // sane default if misconfigured (45 minutes * 60 seconds)
		}

		// Duration lives in the metadata JSON of the *original* item (not variants).
		// The API puts it as `"duration": <seconds as float>`.
		var meta struct {
			Duration float64 `json:"duration"`
		}
		if item.Metadata != "" {
			if err := json.Unmarshal([]byte(item.Metadata), &meta); err == nil && meta.Duration > 0 {
				if meta.Duration > float64(maxDur) {
					d.progressBar.Describe(fmt.Sprintf(
						"[yellow]Skipping long video[reset] %s (%.1f sec > %d sec limit)",
						item.ID, meta.Duration, maxDur,
					))
					logger.Logger.Printf("[INFO] Skipping video %s: duration %.1f sec exceeds limit of %d sec",
						item.ID, meta.Duration, maxDur)
					return nil
				}
			}
		}
	}

	mediaUrl := bestMedia.Locations[0].Location
	parsedURL, err := url.Parse(mediaUrl)
	if err != nil {
		return fmt.Errorf("error parsing URL: %v", err)
	}

	logger.Logger.Printf("Trying to download (%s) %s", bestMedia.Mimetype, mediaUrl)

	ext := filepath.Ext(parsedURL.Path)
	if strings.HasSuffix(mediaUrl, ".m3u8") {
		ext = ".mp4"
	}

	// Generate filename using the new centralized function
	fileName := d.generateFilename(bestMedia, modelName, contentSource, index, isPreview, ext)
	logger.Logger.Printf("[DEBUG] Generated base filename: %s for MediaID: %s", fileName, bestMedia.ID)
	idealPath := filepath.Join(baseDir, subDir, fileName)

	filePath := idealPath

	if !isDiagnosis {
		// TIER 1: Check if already downloaded in DB
		if d.database.FileExists(idealPath) {
			if _, err := os.Stat(idealPath); os.IsNotExist(err) {
				d.progressBar.Describe(fmt.Sprintf("[yellow]File missing[reset], Redownloading %s", fileName))
			} else {
				d.progressBar.Describe(fmt.Sprintf("[red]File Exists[reset], Skipping %s", fileName))
				return nil
			}
		} else {
			// Check disk and check if another goroutine is actively writing here
			fi, err := os.Stat(idealPath)
			_, isActive := activeDownloads.Load(idealPath)

			// Orphan on disk (Not in DB, and NO OTHER thread is currently downloading it) -> Adopt
			if err == nil && !isActive && fi.Size() > 0 {
				d.progressBar.Describe(fmt.Sprintf("[yellow]No DB Record[reset], Adding %s", fileName))
				hashString, err := d.hashExistingFile(idealPath)
				if err != nil {
					logger.Logger.Printf("[ERROR] failed to hash existing file %s: %v", idealPath, err)
					return fmt.Errorf("error hashing existing file: %v", err)
				}
				err = d.database.SaveFile(modelName, hashString, idealPath, fileType, sourceID)
				if err != nil {
					logger.Logger.Printf("[ERROR] failed to save hash for existing file %s: %v", idealPath, err)
					return fmt.Errorf("error saving hash for existing file: %v", err)
				}
				return nil
			} else {
				// Completely new file (or currently colliding with an active download).
				fileName = d.resolveUniqueFilePath(filepath.Join(baseDir, subDir), fileName, ext)
				filePath = filepath.Join(baseDir, subDir, fileName)
			}
		}
	} else {
		// Diagnosis mode
		fileName = d.resolveUniqueFilePath(filepath.Join(baseDir, subDir), fileName, ext)
		filePath = filepath.Join(baseDir, subDir, fileName)
	}

	activeDownloads.Store(filePath, true)
	defer activeDownloads.Delete(filePath)

	d.progressBar.Describe(fmt.Sprintf("[green]Downloading[reset] %s", fileName))

	var dlErr error
	if bestMedia.Mimetype == "application/vnd.apple.mpegurl" && d.ffmpegAvailable {
		fullUrl := mediaUrl
		metadata := bestMedia.Locations[0].Metadata
		var frameRate float64

		if bestMedia.Metadata != "" {
			var meta struct {
				FrameRate float64 `json:"frameRate"`
			}
			if err := json.Unmarshal([]byte(bestMedia.Metadata), &meta); err == nil {
				frameRate = meta.FrameRate
				logger.Logger.Printf("Found frame rate %f for M3U8 stream", frameRate)
			}
		}

		var sourceID string
		if p, ok := contentSource.(posts.Post); ok {
			sourceID = p.ID
		} else if m, ok := contentSource.(posts.Message); ok {
			sourceID = m.ID
		}
		if metadata != nil {
			fullUrl += fmt.Sprintf("?ngsw-bypass=true&Policy=%s&Key-Pair-Id=%s&Signature=%s",
				url.QueryEscape(metadata["Policy"]),
				url.QueryEscape(metadata["Key-Pair-Id"]),
				url.QueryEscape(metadata["Signature"]))
		}

		dlErr = d.DownloadM3U8(ctx, modelName, fullUrl, filePath, sourceID, frameRate, isDiagnosis)
		if dlErr != nil {
			os.Remove(filePath) // Clean up the 0-byte file if ffmpeg fails
		}
	} else {
		dlErr = d.downloadRegularFile(ctx, mediaUrl, filePath, modelName, fileType, sourceID, isDiagnosis)
		if dlErr != nil {
			os.Remove(filePath) // Clean up the 0-byte file if download fails
		}
	}

	if dlErr == nil && d.cfg.Options.ApplyFileTimestamps && !isDiagnosis {
		t := extractTimestamp(contentSource, accountMedia, bestMedia)
		applyFileTimestamp(filePath, t)
	}

	return dlErr
}

func (d *Downloader) downloadWithRetry(ctx context.Context, url string) (*http.Response, error) {
	backoff := time.Second
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			backoff *= 2
		}

		if d.limiter != nil {
			if err := d.limiter.Wait(ctx); err != nil {
				return nil, fmt.Errorf("rate limiter wait error: %v", err)
			}
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("error creating request: %v", err)
		}

		if d.headers != nil {
			d.headers.AddHeadersToRequest(req, true)
		}

		if strings.HasSuffix(url, ".m3u8") {
			req.Header.Add("Accept", "*/*")
			req.Header.Add("Origin", "https://fansly.com")
			req.Header.Add("Referer", "https://fansly.com/")
		}

		resp, err := utils.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			logger.Logger.Printf("Download attempt %d failed: %v", attempt+1, err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			logger.Logger.Printf("Access denied (HTTP %d). This could be a rate limit or missing subscription tier. Retrying...", resp.StatusCode)
		} else {
			logger.Logger.Printf("Download failed with status %d, retrying...", resp.StatusCode)
		}

		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		resp.Body.Close()
	}

	return nil, fmt.Errorf("failed to download %s after %d retries: %v", url, maxRetries, lastErr)
}

func (d *Downloader) downloadRegularFile(ctx context.Context, url, filePath string, modelName string, fileType, postID string, isDiagnosis bool) error {
	resp, err := d.downloadWithRetry(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Update the description of the main bar
	d.progressBar.Describe(fmt.Sprintf("[cyan]Downloading[reset] %s", filepath.Base(filePath)))

	partPath := filePath + ".part"
	out, err := os.Create(partPath)
	if err != nil {
		return err
	}

	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, hash), resp.Body)
	if err != nil {
		out.Close()
		os.Remove(partPath)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(partPath)
		return err
	}

	if err := os.Rename(partPath, filePath); err != nil {
		os.Remove(partPath)
		return err
	}

	hashString := hex.EncodeToString(hash.Sum(nil))
	if !isDiagnosis {
		return d.database.SaveFile(modelName, hashString, filePath, fileType, postID)
	}

	return nil
}

func (d *Downloader) hashExistingFile(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func (d *Downloader) Close() error {
	if d.database == nil {
		return nil
	}
	return d.database.Close()
}

// Reload updates the downloader with new configuration settings.
func (d *Downloader) Reload(cfg *config.Config) error {
	fanslyHeaders, err := headers.NewFanslyHeaders(cfg)
	if err != nil {
		return err
	}
	if cfg.Options.SaveLocation != d.saveLocation {
		database, err := db.NewDatabase(cfg.Options.SaveLocation)
		if err != nil {
			return err
		}
		if d.database != nil {
			d.database.Close()
		}
		d.database = database
	}
	d.cfg = cfg
	d.saveLocation = cfg.Options.SaveLocation
	d.M3U8Download = cfg.Options.M3U8Download
	d.headers = fanslyHeaders
	headers.InvalidateCachedHeaders()
	return nil
}
