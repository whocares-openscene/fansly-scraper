package download

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/agnosto/fansly-scraper/db/models"
	"github.com/agnosto/fansly-scraper/logger"
)

// mediaSubdirByType maps a file type recorded in the database to the folder
// the downloader stores that kind of media in.
var mediaSubdirByType = map[string]string{
	"image": "images",
	"video": "videos",
	"audio": "audios",
}

// hasCopyableFiles reports whether every file recorded for postID still exists
// on disk. That is the precondition for copying a post instead of downloading
// it again.
func (d *Downloader) hasCopyableFiles(postID string) bool {
	files, err := d.database.FilesForPost(postID)
	if err != nil {
		logger.Logger.Printf("[WARN] Failed to look up recorded files for post %s: %v", postID, err)
		return false
	}
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !isNonEmptyFile(f.Path) {
			return false
		}
	}
	return true
}

// copyExistingPostFiles copies every file recorded for an already processed
// post from its previous location into baseDir. This lets a new save_location
// be populated without downloading the media again. Files that already exist at
// the destination are left untouched, so repeated runs stay cheap.
func (d *Downloader) copyExistingPostFiles(postID, modelName, baseDir string) (int, error) {
	files, err := d.database.FilesForPost(postID)
	if err != nil {
		return 0, fmt.Errorf("failed to look up files for post %s: %w", postID, err)
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("no files recorded for post %s", postID)
	}

	copied := 0
	for _, f := range files {
		subDir := destinationSubDir(f)
		if subDir == "" {
			return copied, fmt.Errorf("unknown media type %q for %s", f.FileType, f.Path)
		}

		destDir := filepath.Join(baseDir, subDir)
		dest := filepath.Join(destDir, filepath.Base(f.Path))
		if isNonEmptyFile(dest) {
			continue
		}

		srcInfo, statErr := os.Stat(f.Path)
		if statErr != nil || srcInfo.Size() == 0 {
			return copied, fmt.Errorf("source file %s is missing or empty", f.Path)
		}

		if err := os.MkdirAll(destDir, os.ModePerm); err != nil {
			return copied, fmt.Errorf("failed to create %s: %w", destDir, err)
		}
		if err := copyFile(f.Path, dest); err != nil {
			return copied, err
		}
		d.recordCopy(f.FileType)
		if d.cfg.Options.ApplyFileTimestamps {
			applyFileTimestamp(dest, srcInfo.ModTime())
		}
		copied++
	}

	logger.Logger.Printf("[INFO] [%s] Copied %d file(s) for post %s to %s", modelName, copied, postID, baseDir)
	return copied, nil
}

// copyPreviouslyDownloadedPost copies an individually requested post from the
// location it was originally saved to into the current save_location.
func (d *Downloader) copyPreviouslyDownloadedPost(postID string) (int, error) {
	post, err := d.database.GetProcessedPost(postID)
	if err != nil {
		return 0, err
	}
	if post == nil || post.ModelUsername == "" {
		return 0, fmt.Errorf("no recorded model for post %s", postID)
	}

	baseDir := filepath.Join(d.saveLocation, strings.ToLower(post.ModelUsername), "timeline")
	for _, subDir := range []string{"images", "videos", "audios"} {
		dir := filepath.Join(baseDir, subDir)
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return 0, fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	return d.copyExistingPostFiles(postID, post.ModelUsername, baseDir)
}

// destinationSubDir returns the media folder a recorded file belongs in. The
// recorded parent directory is preferred so a copy keeps the original
// images/videos/audios layout, falling back to the recorded file type.
func destinationSubDir(f models.File) string {
	switch parent := filepath.Base(filepath.Dir(f.Path)); parent {
	case "images", "videos", "audios":
		return parent
	}
	return mediaSubdirByType[f.FileType]
}

// isNonEmptyFile reports whether path is a regular file with content.
func isNonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// copyFile copies src to dst through a .part file, so an interrupted copy never
// leaves a truncated file at the destination.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", src, err)
	}
	defer in.Close()

	part := dst + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", part, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(part)
		return fmt.Errorf("failed to copy %s to %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return fmt.Errorf("failed to close %s: %w", part, err)
	}
	if err := os.Rename(part, dst); err != nil {
		os.Remove(part)
		return fmt.Errorf("failed to move %s into place: %w", part, err)
	}
	return nil
}
