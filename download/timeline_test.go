package download

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agnosto/fansly-scraper/config"
	"github.com/agnosto/fansly-scraper/headers"
	"github.com/agnosto/fansly-scraper/utils"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadTimelineStopsWhenCancelled(t *testing.T) {
	requests := 0
	old := utils.HTTPClient.Transport
	defer func() { utils.HTTPClient.Transport = old }()
	utils.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`)), Header: http.Header{}, Request: r}, nil
	})
	d := &Downloader{cfg: config.CreateDefaultConfig(), headers: &headers.FanslyHeaders{}, saveLocation: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.DownloadTimeline(ctx, "model", "creator", "", ""); !errors.Is(err, context.Canceled) || requests != 0 {
		t.Fatalf("cancelled timeline sent %d requests and returned %v", requests, err)
	}
}

func TestReloadRefreshesCachedHeaders(t *testing.T) {
	config.SetConfigPath(filepath.Join(t.TempDir(), "config.toml"))
	defer config.SetConfigPath("")
	save := func(token string) *config.Config {
		cfg := config.CreateDefaultConfig()
		cfg.Account.AuthToken, cfg.Account.UserAgent = token, "agent"
		cfg.SecurityHeaders.DeviceID, cfg.SecurityHeaders.SessionID, cfg.SecurityHeaders.CheckKey = "device", "session", "key"
		if err := config.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	cfg := save("token-a")
	if h, err := headers.GetCachedHeaders(); err != nil || h.AuthToken != "token-a" {
		t.Fatalf("initial cached headers: %v", err)
	}
	d := &Downloader{saveLocation: cfg.Options.SaveLocation}
	if err := d.Reload(save("token-b")); err != nil {
		t.Fatal(err)
	}
	if h, err := headers.GetCachedHeaders(); err != nil || h.AuthToken != "token-b" {
		t.Fatalf("cached headers after reload use the previous account (err %v)", err)
	}
}
