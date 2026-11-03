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

func (d *Downloader) DownloadMessages(ctx context.Context, modelId, modelName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	messagesWithMedia, err := posts.GetAllMessagesWithMedia(ctx, modelId, d.headers, d.cfg.Options.PostLimit)
	if err != nil {
		logger.Logger.Printf("[ERROR] [%s] Failed to get message media: %v", modelName, err)
		return err
	}

	totalMediaItems := 0
	for _, msg := range messagesWithMedia {
		totalMediaItems += len(msg.Media)
	}

	d.reportProgress(ProgressUpdate{Type: "status", Message: fmt.Sprintf("Found %d message media items", totalMediaItems), Current: 0, Total: totalMediaItems})

	if totalMediaItems == 0 {
		logger.Logger.Printf("[INFO] [%s] No new message media to download.", modelName)
		d.reportProgress(ProgressUpdate{Type: "progress", Percent: 100, Message: "No message media to download", Current: 0, Total: 0})
		return nil
	}

	baseDir := filepath.Join(d.saveLocation, strings.ToLower(modelName), "messages")
	for _, subDir := range []string{"images", "videos", "audios"} {
		if err = os.MkdirAll(filepath.Join(baseDir, subDir), os.ModePerm); err != nil {
			return err
		}
	}

	d.progressBar = progressbar.NewOptions(totalMediaItems,
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionUseANSICodes(true),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetDescription("[green]Downloading Messages[reset]"),
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

	for _, msgWithMedia := range messagesWithMedia {
		// The index `i` is for each media item within a single message.
		for i, mediaItem := range msgWithMedia.Media {
			wg.Add(1)
			go func(media posts.AccountMedia, message posts.Message, index int) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				d.reportProgress(ProgressUpdate{Type: "post_start", Message: fmt.Sprintf("Downloading message media %s", media.ID), Current: completedItems, Total: totalMediaItems})

				err := d.DownloadMediaItem(ctx, media, baseDir, modelName, message, index)
				if err != nil {
					logger.Logger.Printf("[ERROR] [%s] Failed to download message media item %s: %v", modelName, media.ID, err)
				}

				completedMu.Lock()
				completedItems++
				percent := 0
				if totalMediaItems > 0 {
					percent = (completedItems * 100) / totalMediaItems
				}
				d.reportProgress(ProgressUpdate{Type: "progress", Percent: percent, Message: fmt.Sprintf("Completed %d/%d message items", completedItems, totalMediaItems), Current: completedItems, Total: totalMediaItems})
				completedMu.Unlock()

				d.progressBar.Add(1)
			}(mediaItem, msgWithMedia.Message, i)
		}
	}

	wg.Wait()
	d.progressBar.Clear()
	d.reportProgress(ProgressUpdate{Type: "progress", Percent: 100, Message: "Messages download complete", Current: totalMediaItems, Total: totalMediaItems})
	return ctx.Err()
}
