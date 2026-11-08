package config

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

var unsafeChars = regexp.MustCompile(`[^\p{L}\p{N}\s\-._]`)

type Config struct {
	Account         AccountConfig         `toml:"account"`
	Options         OptionsConfig         `toml:"options"`
	LiveSettings    LiveSettingsConfig    `toml:"live_settings"`
	Notifications   NotificationsConfig   `toml:"notifications"`
	SecurityHeaders SecurityHeadersConfig `toml:"security_headers"`
}

type LiveSettingsConfig struct {
	SaveLocation            string `toml:"save_location"`
	VODsFileExtension       string `toml:"vods_file_extension"`
	FFmpegConvert           bool   `toml:"ffmpeg_convert"`
	GenerateContactSheet    bool   `toml:"generate_contact_sheet"`
	UseMTForContactSheet    bool   `toml:"use_mt_for_contact_sheet"`
	FilenameTemplate        string `toml:"filename_template"`
	DateFormat              string `toml:"date_format"`
	RecordChat              bool   `toml:"record_chat"`
	FFmpegRecordingOptions  string `toml:"ffmpeg_recording_options"`
	FFmpegConversionOptions string `toml:"ffmpeg_conversion_options"`
	CheckInterval           int    `toml:"check_interval"`
}

type AccountConfig struct {
	AuthToken string `toml:"auth_token"`
	UserAgent string `toml:"user_agent"`
}

type OptionsConfig struct {
	SaveLocation            string `toml:"save_location"`
	M3U8Download            bool   `toml:"m3u8_dl"`
	CheckUpdates            bool   `toml:"check_updates"`
	SkipPreviews            bool   `toml:"skip_previews"`
	UseContentAsFilename    bool   `toml:"use_content_as_filename"`
	ContentFilenameTemplate string `toml:"content_filename_template"`
	ContentFilenameLength   int    `toml:"content_filename_length"`
	DateFormat              string `toml:"date_format"`
	DownloadMediaType       string `toml:"download_media_type"`
	SkipDownloadedPosts     bool   `toml:"skip_downloaded_posts"`
	CopyDownloadedPosts     bool   `toml:"copy_downloaded_posts"`
	DownloadProfilePic      bool   `toml:"download_profile_pic"`
	PostLimit               int    `toml:"post_limit"`
	SkipLongVideos          bool   `toml:"skip_long_videos"`
	MaxVideoDurationSeconds int    `toml:"max_video_duration_seconds"`
	ApplyFileTimestamps     bool   `toml:"apply_file_timestamps"`
}

type NotificationsConfig struct {
	Enabled                   bool   `toml:"enabled"`
	SystemNotify              bool   `toml:"system_notify"`
	DiscordWebhook            string `toml:"discord_webhook"`
	DiscordMentionID          string `toml:"discord_mention_id"`
	TelegramBotToken          string `toml:"telegram_bot_token"`
	TelegramChatID            string `toml:"telegram_chat_id"`
	NotifyOnLiveStart         bool   `toml:"notify_on_live_start"`
	NotifyOnLiveEnd           bool   `toml:"notify_on_live_end"`
	SendContactSheetOnLiveEnd bool   `toml:"send_contact_sheet_on_live_end"`
}

type SecurityHeadersConfig struct {
	DeviceID    string    `toml:"device_id"`
	SessionID   string    `toml:"session_id"`
	CheckKey    string    `toml:"check_key"`
	LastUpdated time.Time `toml:"last_updated"`
}

type Account struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

var (
	customConfigPath string
	customConfigDir  string
)

// SetConfigPath overrides the config file path and its enclosing directory.
func SetConfigPath(path string) {
	if path == "" {
		customConfigPath = ""
		customConfigDir = ""
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	customConfigPath = absPath
	customConfigDir = filepath.Dir(absPath)
}

// SetProfile sets the active profile name, placing config in profiles/<name>/config.toml.
func SetProfile(profile string) {
	if profile == "" {
		customConfigPath = ""
		customConfigDir = ""
		return
	}
	baseDir := getDefaultConfigDir()
	profileDir := filepath.Join(baseDir, "profiles", profile)
	customConfigDir = profileDir
	customConfigPath = filepath.Join(profileDir, "config.toml")
}

func GetConfigPath() string {
	if customConfigPath != "" {
		return customConfigPath
	}
	currentDirConfig := "config.toml"
	if _, err := os.Stat(currentDirConfig); err == nil {
		return currentDirConfig
	}
	return filepath.Join(GetConfigDir(), "config.toml")
}

func GetConfigDir() string {
	if customConfigDir != "" {
		return customConfigDir
	}
	return getDefaultConfigDir()
}

func getDefaultConfigDir() string {
	var configDir string
	var err error

	if runtime.GOOS == "darwin" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		configDir = filepath.Join(homeDir, ".config")
	} else {
		configDir, err = os.UserConfigDir()
		if err != nil {
			log.Fatal(err)
		}
	}

	return filepath.Join(configDir, "fansly-scraper")
}

// saveMu serializes writers within this process: monitor goroutines can
// trigger concurrent security-header refreshes that each call SaveConfig.
var saveMu sync.Mutex

func SaveConfig(cfg *Config) error {
	saveMu.Lock()
	defer saveMu.Unlock()

	configPath := GetConfigPath()

	// Try to load existing config to merge with
	existingConfig, err := LoadConfig(configPath)
	if err == nil {
		cfg = MergeConfigs(existingConfig, cfg)
	}

	return writeConfig(configPath, cfg)
}

// writeConfig replaces path through a temporary file so a crash or concurrent
// reader never sees a half-written config. CreateTemp makes the file
// owner-only, which matters because the config holds the auth token.
func writeConfig(path string, cfg *Config) error {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := file.Name()
	if err := toml.NewEncoder(file).Encode(cfg); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func LoadConfig(configPath string) (*Config, error) {
	var config Config
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		return nil, err
	}

	if err := ValidateConfig(&config, configPath); err != nil {
		return nil, err
	}

	return &config, nil
}

func CreateDefaultConfig() *Config {
	return &Config{
		Account: AccountConfig{
			AuthToken: "",
			UserAgent: "",
		},
		Options: OptionsConfig{
			SaveLocation:            "/path/to/save/content/to",
			M3U8Download:            false,
			CheckUpdates:            false,
			SkipPreviews:            true,
			UseContentAsFilename:    false,
			ContentFilenameTemplate: "{date}-{content}_{index}",
			ContentFilenameLength:   50,
			DateFormat:              "20060102",
			DownloadMediaType:       "all",
			SkipDownloadedPosts:     false,
			CopyDownloadedPosts:     false,
			DownloadProfilePic:      true,
			PostLimit:               0,
			SkipLongVideos:          false,
			MaxVideoDurationSeconds: 2700,
			ApplyFileTimestamps:     false,
		},
		LiveSettings: LiveSettingsConfig{
			SaveLocation:            "", // Empty means use default path
			VODsFileExtension:       ".ts",
			FFmpegConvert:           true,
			GenerateContactSheet:    true,
			UseMTForContactSheet:    false,
			FilenameTemplate:        "{model_username}_{date}_{streamId}_{streamVersion}",
			DateFormat:              "20060102_150405",
			RecordChat:              true,
			FFmpegRecordingOptions:  "",
			FFmpegConversionOptions: "",
			CheckInterval:           60,
		},
		Notifications: NotificationsConfig{
			Enabled:                   false,
			SystemNotify:              true,
			DiscordWebhook:            "",
			DiscordMentionID:          "",
			TelegramBotToken:          "",
			TelegramChatID:            "",
			NotifyOnLiveStart:         true,
			NotifyOnLiveEnd:           false,
			SendContactSheetOnLiveEnd: false,
		},
		SecurityHeaders: SecurityHeadersConfig{
			DeviceID:    "",
			SessionID:   "",
			CheckKey:    "",
			LastUpdated: time.Now(),
		},
	}
}

func ValidateConfig(cfg *Config, configPath string) error {
	if cfg.Account.UserAgent == "" {
		return fmt.Errorf("user_agent is empty in %v", configPath)
	}
	if cfg.Account.AuthToken == "" {
		return fmt.Errorf("auth_token is empty in %v", configPath)
	}
	if cfg.Options.SaveLocation == "" {
		return fmt.Errorf("save_location is empty in %v", configPath)
	}
	cfg.Options.SaveLocation = filepath.Clean(cfg.Options.SaveLocation)
	if cfg.LiveSettings.SaveLocation != "" {
		cfg.LiveSettings.SaveLocation = filepath.Clean(cfg.LiveSettings.SaveLocation)
	}
	if cfg.SecurityHeaders.LastUpdated.IsZero() {
		cfg.SecurityHeaders.LastUpdated = time.Now()
	}
	return nil
}

func OpenConfigInEditor(configPath string) error {
	var cmd *exec.Cmd
	editor := os.Getenv("EDITOR")
	if editor == "" {
		switch runtime.GOOS {
		case "windows":
			editor = "notepad"
		case "darwin":
			editor = "open"
		default:
			editor = "vim"
		}
	}

	if runtime.GOOS == "windows" {
		cmd = exec.Command(editor, configPath)
	} else if runtime.GOOS == "darwin" && editor == "open" {
		cmd = exec.Command(editor, "-t", configPath) // open with default text editor
	} else {
		cmd = exec.Command(editor, configPath)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func SanitizeFilename(filename string) string {
	filename = strings.ReplaceAll(filename, " ", "_")

	// Replace ':' before the regex pass, which would otherwise strip it.
	filename = strings.ReplaceAll(filename, ":", "-")

	filename = unsafeChars.ReplaceAllString(filename, "")

	problematicChars := []string{"/", "\\", "?", "%", "*", "|", "\"", "<", ">"}
	for _, char := range problematicChars {
		filename = strings.ReplaceAll(filename, char, "_")
	}

	if filename == "" {
		return "empty_content"
	}

	return filename
}

func FormatVODFilename(template string, data map[string]string) string {
	result := template
	if version, exists := data["streamVersion"]; exists {
		data["streamVersion"] = "v" + version
	}

	for key, value := range data {
		placeholder := fmt.Sprintf("{%s}", key)
		sanitizedValue := SanitizeFilename(value)
		result = strings.ReplaceAll(result, placeholder, sanitizedValue)
	}

	return SanitizeFilename(result)
}

func ResolveLiveSavePath(cfg *Config, username string) string {
	if cfg.LiveSettings.SaveLocation != "" {
		return cfg.LiveSettings.SaveLocation
	}
	return filepath.Join(cfg.Options.SaveLocation, strings.ToLower(username), "lives")
}

func GetVODFilename(cfg *Config, data map[string]string) string {
	dateFormat := cfg.LiveSettings.DateFormat
	if dateFormat == "" {
		dateFormat = "20060102_150405"
	}
	data["date"] = time.Now().Format(dateFormat)

	template := cfg.LiveSettings.FilenameTemplate
	if template == "" {
		template = "{model_username}_{date}_{streamId}"
	}

	filename := FormatVODFilename(template, data)
	return filename + cfg.LiveSettings.VODsFileExtension
}

func BuildVODPath(cfg *Config, username string, streamData map[string]string) string {
	savePath := ResolveLiveSavePath(cfg, username)
	filename := GetVODFilename(cfg, streamData)
	return filepath.Join(savePath, filename)
}
