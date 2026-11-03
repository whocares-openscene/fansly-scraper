package download

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agnosto/fansly-scraper/logger"
	"github.com/agnosto/fansly-scraper/posts"
	"github.com/schollz/progressbar/v3"
)

func (d *Downloader) DownloadStories(ctx context.Context, modelId, modelName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stories, storyMediaItems, err := posts.GetModelStories(ctx, modelId, d.headers)
	if err != nil {
		logger.Logger.Printf("[ERROR] [%s] Failed to get story media: %v", modelName, err)
		return err
	}

	storyMap := make(map[string]posts.Story)
	for _, story := range stories {
		storyMap[story.ContentID] = story
	}

	d.reportProgress(ProgressUpdate{Type: "status", Message: fmt.Sprintf("Found %d story media items", len(storyMediaItems)), Current: 0, Total: len(storyMediaItems)})

	if len(storyMediaItems) == 0 {
		logger.Logger.Printf("[INFO] [%s] No new story media to download.", modelName)
		d.reportProgress(ProgressUpdate{Type: "progress", Percent: 100, Message: "No story media to download", Current: 0, Total: 0})
		return nil
	}

	baseDir := filepath.Join(d.saveLocation, strings.ToLower(modelName), "stories")
	for _, subDir := range []string{"images", "videos", "audios"} {
		if err = os.MkdirAll(filepath.Join(baseDir, subDir), os.ModePerm); err != nil {
			return err
		}
	}

	d.progressBar = progressbar.NewOptions(len(storyMediaItems),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionUseANSICodes(true),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetDescription("[green]Downloading Stories[reset]"),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(15*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
	)

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 10)

	completedItems := 0
	var completedMu sync.Mutex
	totalItems := len(storyMediaItems)

	for i, mediaItem := range storyMediaItems {
		var contentSource any
		if story, ok := storyMap[mediaItem.ID]; ok {
			contentSource = story
		}
		wg.Add(1)
		go func(media posts.AccountMedia, source any, index int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			d.reportProgress(ProgressUpdate{Type: "post_start", Message: fmt.Sprintf("Downloading story media %s", media.ID), Current: completedItems, Total: totalItems})

			err := d.downloadStoryMediaItem(ctx, media, baseDir, modelName, source, index)
			if err != nil {
				logger.Logger.Printf("[ERROR] [%s] Failed to download story media item %s: %v", modelName, media.ID, err)
			}

			completedMu.Lock()
			completedItems++
			percent := 0
			if totalItems > 0 {
				percent = (completedItems * 100) / totalItems
			}
			d.reportProgress(ProgressUpdate{Type: "progress", Percent: percent, Message: fmt.Sprintf("Completed %d/%d story items", completedItems, totalItems), Current: completedItems, Total: totalItems})
			completedMu.Unlock()

			d.progressBar.Add(1)
		}(mediaItem, contentSource, i)
	}

	wg.Wait()
	d.progressBar.Clear()
	d.reportProgress(ProgressUpdate{Type: "progress", Percent: 100, Message: "Stories download complete", Current: totalItems, Total: totalItems})
	return ctx.Err()
}

func (d *Downloader) downloadStoryMediaItem(ctx context.Context, accountMedia posts.AccountMedia, baseDir, modelName string, contentSource any, index int) error {
	// Download main media - pass contentSource and the index
	err := d.downloadSingleItem(ctx, accountMedia, accountMedia.Media, baseDir, modelName, false, contentSource, index, false)
	if err != nil {
		return fmt.Errorf("error downloading main media: %v", err)
	}

	// Download preview if it exists
	if !d.cfg.Options.SkipPreviews && accountMedia.Preview != nil {
		err = d.downloadSingleItem(ctx, accountMedia, *accountMedia.Preview, baseDir, modelName, true, contentSource, index, false)
		if err != nil {
			return fmt.Errorf("error downloading preview: %v", err)
		}
	}

	return nil
}
