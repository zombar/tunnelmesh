package update

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config holds the configuration for the updater.
type Config struct {
	GitHubOwner string        // GitHub repository owner (e.g., "zombar")
	GitHubRepo  string        // GitHub repository name (e.g., "tunnelmesh")
	BinaryName  string        // Binary name (defaults to "tunnelmesh")
	Timeout     time.Duration // HTTP timeout (defaults to 30s)
	BaseURL     string        // Base URL for API (defaults to GitHub API, used for testing)
}

// Asset represents a release asset (binary or checksums file).
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"browser_download_url"`
}

// ReleaseInfo contains information about a GitHub release.
type ReleaseInfo struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Updater handles checking for updates and downloading new versions.
type Updater struct {
	config Config
	client *http.Client
}

// NewUpdater creates a new Updater with the given configuration.
func NewUpdater(cfg Config) *Updater {
	if cfg.BinaryName == "" {
		cfg.BinaryName = "tunnelmesh"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}

	return &Updater{
		config: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// CheckLatest fetches the latest release information from GitHub.
func (u *Updater) CheckLatest() (*ReleaseInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest",
		u.config.BaseURL, u.config.GitHubOwner, u.config.GitHubRepo)

	return u.fetchRelease(url)
}

// CheckVersion fetches a specific version's release information from GitHub.
func (u *Updater) CheckVersion(version string) (*ReleaseInfo, error) {
	// Ensure version has 'v' prefix for GitHub tag
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
		u.config.BaseURL, u.config.GitHubOwner, u.config.GitHubRepo, version)

	return u.fetchRelease(url)
}

// fetchRelease fetches release information from the given URL.
func (u *Updater) fetchRelease(url string) (*ReleaseInfo, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "tunnelmesh-updater")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("release not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var release ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &release, nil
}

// GetAssetName returns the expected asset name for the given OS and architecture.
func GetAssetName(goos, goarch string) string {
	name := fmt.Sprintf("tunnelmesh-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// FindAsset finds the binary asset for the given OS and architecture.
func (r *ReleaseInfo) FindAsset(goos, goarch string) (*Asset, error) {
	assetName := GetAssetName(goos, goarch)

	for i := range r.Assets {
		if r.Assets[i].Name == assetName {
			return &r.Assets[i], nil
		}
	}

	return nil, fmt.Errorf("asset %q not found in release %s", assetName, r.TagName)
}

// FindChecksums finds the checksums.txt asset in the release.
func (r *ReleaseInfo) FindChecksums() (*Asset, error) {
	for i := range r.Assets {
		if r.Assets[i].Name == "checksums.txt" {
			return &r.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("checksums.txt not found in release %s", r.TagName)
}

// ProgressFunc is called during download with progress information.
type ProgressFunc func(downloaded, total int64)

// Download downloads the asset to a temporary file and returns the path.
func (u *Updater) Download(asset *Asset, progressFn ProgressFunc) (string, error) {
	req, err := http.NewRequest("GET", asset.DownloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "tunnelmesh-updater")

	// Use a longer timeout for downloads
	client := &http.Client{
		Timeout: 10 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "tunnelmesh-update-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	// Get content length
	var total int64
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	} else if asset.Size > 0 {
		total = asset.Size
	}

	// Create progress reader if callback provided
	var reader io.Reader = resp.Body
	if progressFn != nil {
		reader = &progressReader{
			reader:     resp.Body,
			total:      total,
			progressFn: progressFn,
		}
	}

	// Copy to temp file
	if _, err := io.Copy(tmpFile, reader); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("close temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// progressReader wraps a reader to report download progress.
type progressReader struct {
	reader     io.Reader
	total      int64
	downloaded int64
	progressFn ProgressFunc
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.downloaded += int64(n)
	if r.progressFn != nil {
		r.progressFn(r.downloaded, r.total)
	}
	return n, err
}

// ParseChecksums parses a checksums.txt file content.
// Format: "<sha256>  <filename>" (two spaces between hash and filename)
func ParseChecksums(content string) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Split by double space (SHA256 format)
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			// Try single space as fallback
			parts = strings.Fields(line)
			if len(parts) != 2 {
				continue
			}
		}

		hash := strings.TrimSpace(parts[0])
		filename := strings.TrimSpace(parts[1])
		checksums[filename] = hash
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan checksums: %w", err)
	}

	return checksums, nil
}

// CalculateChecksum calculates the SHA256 checksum of a file.
func CalculateChecksum(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("calculate hash: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyChecksum verifies that a file matches the expected SHA256 hash.
func VerifyChecksum(filePath, expectedHash string) error {
	actualHash, err := CalculateChecksum(filePath)
	if err != nil {
		return err
	}

	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// VerifyDownload verifies the downloaded file against the checksums in the release.
func (u *Updater) VerifyDownload(filePath, assetName string, release *ReleaseInfo) error {
	// Find checksums asset
	checksumAsset, err := release.FindChecksums()
	if err != nil {
		return err
	}

	// Download checksums file
	req, err := http.NewRequest("GET", checksumAsset.DownloadURL, nil)
	if err != nil {
		return fmt.Errorf("create checksum request: %w", err)
	}
	req.Header.Set("User-Agent", "tunnelmesh-updater")

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download checksums failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}

	// Parse checksums
	checksums, err := ParseChecksums(string(body))
	if err != nil {
		return fmt.Errorf("parse checksums: %w", err)
	}

	// Find checksum for this asset
	expectedHash, ok := checksums[assetName]
	if !ok {
		return fmt.Errorf("checksum for %q not found", assetName)
	}

	// Verify
	return VerifyChecksum(filePath, expectedHash)
}

// DownloadChecksums downloads and parses the checksums file from a release.
func (u *Updater) DownloadChecksums(release *ReleaseInfo) (map[string]string, error) {
	checksumAsset, err := release.FindChecksums()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", checksumAsset.DownloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "tunnelmesh-updater")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return ParseChecksums(string(body))
}
