package download

import "fmt"

// SessionStats counts the media handled since the last ResetStats call.
// "Downloaded" files were transferred fresh from Fansly, "copied" files were
// relocated from a previous save location because copy_downloaded_posts is
// enabled.
type SessionStats struct {
	VideosDownloaded int
	ImagesDownloaded int
	VideosCopied     int
	ImagesCopied     int
}

// Total reports how many files were handled during the session.
func (s SessionStats) Total() int {
	return s.VideosDownloaded + s.ImagesDownloaded + s.VideosCopied + s.ImagesCopied
}

// Summary returns a single-line description suitable for logs.
func (s SessionStats) Summary() string {
	return fmt.Sprintf("%d video(s) downloaded, %d photo(s) downloaded, %d video(s) copied, %d photo(s) copied",
		s.VideosDownloaded, s.ImagesDownloaded, s.VideosCopied, s.ImagesCopied)
}

// recordDownload counts a freshly downloaded file by its media type.
func (d *Downloader) recordDownload(fileType string) {
	if d == nil {
		return
	}
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	switch fileType {
	case "video":
		d.stats.VideosDownloaded++
	case "image":
		d.stats.ImagesDownloaded++
	}
}

// recordCopy counts a file copied from a previous save location.
func (d *Downloader) recordCopy(fileType string) {
	if d == nil {
		return
	}
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	switch fileType {
	case "video":
		d.stats.VideosCopied++
	case "image":
		d.stats.ImagesCopied++
	}
}

// Stats returns a snapshot of the current session counters.
func (d *Downloader) Stats() SessionStats {
	if d == nil {
		return SessionStats{}
	}
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	return d.stats
}

// ResetStats clears the session counters so a new job starts from zero.
func (d *Downloader) ResetStats() {
	if d == nil {
		return
	}
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	d.stats = SessionStats{}
}
