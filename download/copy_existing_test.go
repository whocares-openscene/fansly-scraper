package download

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agnosto/fansly-scraper/config"
	"github.com/agnosto/fansly-scraper/db"
)

// newTestDownloader builds a Downloader backed by a throwaway database so the
// copy helpers can be exercised without touching real config or download dirs.
func newTestDownloader(t *testing.T) *Downloader {
	t.Helper()

	config.SetConfigPath(filepath.Join(t.TempDir(), "config.toml"))
	t.Cleanup(func() { config.SetConfigPath("") })

	database, err := db.NewDatabase(t.TempDir())
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	return &Downloader{
		cfg:          config.CreateDefaultConfig(),
		database:     database,
		saveLocation: t.TempDir(),
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func TestCopyExistingPostFiles(t *testing.T) {
	d := newTestDownloader(t)

	oldBase := filepath.Join(t.TempDir(), "creator", "timeline")
	imgSrc := filepath.Join(oldBase, "images", "100_01_200.jpeg")
	vidSrc := filepath.Join(oldBase, "videos", "100_02_300.mp4")
	writeTestFile(t, imgSrc, "image-bytes")
	writeTestFile(t, vidSrc, "video-bytes")

	for _, f := range []struct {
		hash, path, fileType string
	}{
		{"hash-img", imgSrc, "image"},
		{"hash-vid", vidSrc, "video"},
	} {
		if err := d.database.SaveFile("creator", f.hash, f.path, f.fileType, "100"); err != nil {
			t.Fatalf("failed to record %s: %v", f.path, err)
		}
	}
	if err := d.database.MarkPostAsProcessed("100", "creator", "content", 100); err != nil {
		t.Fatalf("failed to mark post processed: %v", err)
	}

	if !d.hasCopyableFiles("100") {
		t.Fatal("expected recorded files to be copyable")
	}

	newBase := filepath.Join(t.TempDir(), "creator", "timeline")
	copied, err := d.copyExistingPostFiles("100", "creator", newBase)
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if copied != 2 {
		t.Fatalf("expected 2 files copied, got %d", copied)
	}
	stats := d.Stats()
	if stats.ImagesCopied != 1 || stats.VideosCopied != 1 {
		t.Fatalf("unexpected copy stats: %+v", stats)
	}
	if stats.VideosDownloaded != 0 || stats.ImagesDownloaded != 0 {
		t.Fatalf("copies must not count as downloads: %+v", stats)
	}

	for _, want := range []string{
		filepath.Join(newBase, "images", "100_01_200.jpeg"),
		filepath.Join(newBase, "videos", "100_02_300.mp4"),
	} {
		got, err := os.ReadFile(want)
		if err != nil {
			t.Fatalf("expected copied file %s: %v", want, err)
		}
		if len(got) == 0 {
			t.Fatalf("copied file %s is empty", want)
		}
	}

	// A second run must not copy anything again.
	copied, err = d.copyExistingPostFiles("100", "creator", newBase)
	if err != nil {
		t.Fatalf("second copy failed: %v", err)
	}
	if copied != 0 {
		t.Fatalf("expected no files copied on the second run, got %d", copied)
	}
	if stats := d.Stats(); stats.ImagesCopied != 1 || stats.VideosCopied != 1 {
		t.Fatalf("second run must not change copy stats: %+v", stats)
	}

	d.ResetStats()
	if stats := d.Stats(); stats.Total() != 0 {
		t.Fatalf("reset left stats behind: %+v", stats)
	}
}

func TestHasCopyableFilesRejectsMissingSource(t *testing.T) {
	d := newTestDownloader(t)

	src := filepath.Join(t.TempDir(), "timeline", "images", "1_01_2.jpeg")
	writeTestFile(t, src, "bytes")
	if err := d.database.SaveFile("creator", "hash-1", src, "image", "1"); err != nil {
		t.Fatalf("failed to record file: %v", err)
	}
	if !d.hasCopyableFiles("1") {
		t.Fatal("expected file to be copyable while it exists")
	}

	if err := os.Remove(src); err != nil {
		t.Fatalf("failed to remove source: %v", err)
	}
	if d.hasCopyableFiles("1") {
		t.Fatal("expected a missing source file to prevent copying")
	}
}

func TestCopyPreviouslyDownloadedPostUsesRecordedModel(t *testing.T) {
	d := newTestDownloader(t)

	src := filepath.Join(t.TempDir(), "Creator", "timeline", "videos", "55_01_66.mp4")
	writeTestFile(t, src, "video-bytes")
	if err := d.database.SaveFile("Creator", "hash-55", src, "video", "55"); err != nil {
		t.Fatalf("failed to record file: %v", err)
	}
	if err := d.database.MarkPostAsProcessed("55", "Creator", "content", 100); err != nil {
		t.Fatalf("failed to mark post processed: %v", err)
	}

	copied, err := d.copyPreviouslyDownloadedPost("55")
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if copied != 1 {
		t.Fatalf("expected 1 file copied, got %d", copied)
	}
	if stats := d.Stats(); stats.VideosCopied != 1 {
		t.Fatalf("expected 1 copied video, got %+v", stats)
	}

	want := filepath.Join(d.saveLocation, "creator", "timeline", "videos", "55_01_66.mp4")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s to exist: %v", want, err)
	}
}
