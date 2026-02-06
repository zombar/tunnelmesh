package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewUpdater(t *testing.T) {
	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
	})

	if u == nil {
		t.Fatal("NewUpdater returned nil")
	}
	if u.config.GitHubOwner != "testowner" {
		t.Errorf("GitHubOwner = %q, want %q", u.config.GitHubOwner, "testowner")
	}
	if u.config.GitHubRepo != "testrepo" {
		t.Errorf("GitHubRepo = %q, want %q", u.config.GitHubRepo, "testrepo")
	}
}

func TestUpdaterCheckLatest(t *testing.T) {
	// Create a mock server that returns GitHub release JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/testowner/testrepo/releases/latest" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"tag_name": "v1.2.3",
				"name": "Release v1.2.3",
				"published_at": "2024-01-15T10:00:00Z",
				"assets": [
					{
						"name": "tunnelmesh-linux-amd64",
						"size": 15000000,
						"browser_download_url": "https://example.com/tunnelmesh-linux-amd64"
					},
					{
						"name": "tunnelmesh-darwin-arm64",
						"size": 14000000,
						"browser_download_url": "https://example.com/tunnelmesh-darwin-arm64"
					},
					{
						"name": "checksums.txt",
						"size": 500,
						"browser_download_url": "https://example.com/checksums.txt"
					}
				]
			}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
		BaseURL:     server.URL,
	})

	release, err := u.CheckLatest()
	if err != nil {
		t.Fatalf("CheckLatest() error: %v", err)
	}

	if release.TagName != "v1.2.3" {
		t.Errorf("TagName = %q, want %q", release.TagName, "v1.2.3")
	}
	if len(release.Assets) != 3 {
		t.Errorf("len(Assets) = %d, want %d", len(release.Assets), 3)
	}
}

func TestUpdaterCheckVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/testowner/testrepo/releases/tags/v1.0.0" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"tag_name": "v1.0.0",
				"name": "Release v1.0.0",
				"published_at": "2024-01-01T10:00:00Z",
				"assets": [
					{
						"name": "tunnelmesh-linux-amd64",
						"size": 14000000,
						"browser_download_url": "https://example.com/v1.0.0/tunnelmesh-linux-amd64"
					}
				]
			}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
		BaseURL:     server.URL,
	})

	release, err := u.CheckVersion("v1.0.0")
	if err != nil {
		t.Fatalf("CheckVersion() error: %v", err)
	}

	if release.TagName != "v1.0.0" {
		t.Errorf("TagName = %q, want %q", release.TagName, "v1.0.0")
	}
}

func TestUpdaterCheckVersionNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"message": "Not Found"}`)
	}))
	defer server.Close()

	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
		BaseURL:     server.URL,
	})

	_, err := u.CheckVersion("v9.9.9")
	if err == nil {
		t.Fatal("CheckVersion() expected error for non-existent version")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestGetAssetName(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"linux", "amd64", "tunnelmesh-linux-amd64"},
		{"linux", "arm64", "tunnelmesh-linux-arm64"},
		{"darwin", "amd64", "tunnelmesh-darwin-amd64"},
		{"darwin", "arm64", "tunnelmesh-darwin-arm64"},
		{"windows", "amd64", "tunnelmesh-windows-amd64.exe"},
	}

	for _, tt := range tests {
		t.Run(tt.goos+"_"+tt.goarch, func(t *testing.T) {
			got := GetAssetName(tt.goos, tt.goarch)
			if got != tt.want {
				t.Errorf("GetAssetName(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}

func TestReleaseInfoFindAsset(t *testing.T) {
	release := &ReleaseInfo{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: "tunnelmesh-linux-amd64", DownloadURL: "https://example.com/linux-amd64"},
			{Name: "tunnelmesh-darwin-arm64", DownloadURL: "https://example.com/darwin-arm64"},
			{Name: "checksums.txt", DownloadURL: "https://example.com/checksums.txt"},
		},
	}

	tests := []struct {
		name    string
		goos    string
		goarch  string
		wantURL string
		wantErr bool
	}{
		{
			name:    "find linux amd64",
			goos:    "linux",
			goarch:  "amd64",
			wantURL: "https://example.com/linux-amd64",
		},
		{
			name:    "find darwin arm64",
			goos:    "darwin",
			goarch:  "arm64",
			wantURL: "https://example.com/darwin-arm64",
		},
		{
			name:    "not found",
			goos:    "windows",
			goarch:  "amd64",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, err := release.FindAsset(tt.goos, tt.goarch)
			if tt.wantErr {
				if err == nil {
					t.Error("FindAsset() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("FindAsset() error: %v", err)
			}
			if asset.DownloadURL != tt.wantURL {
				t.Errorf("DownloadURL = %q, want %q", asset.DownloadURL, tt.wantURL)
			}
		})
	}
}

func TestReleaseInfoFindChecksums(t *testing.T) {
	release := &ReleaseInfo{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: "tunnelmesh-linux-amd64", DownloadURL: "https://example.com/linux-amd64"},
			{Name: "checksums.txt", DownloadURL: "https://example.com/checksums.txt"},
		},
	}

	asset, err := release.FindChecksums()
	if err != nil {
		t.Fatalf("FindChecksums() error: %v", err)
	}
	if asset.Name != "checksums.txt" {
		t.Errorf("Name = %q, want %q", asset.Name, "checksums.txt")
	}

	// Test missing checksums
	releaseNoChecksums := &ReleaseInfo{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: "tunnelmesh-linux-amd64", DownloadURL: "https://example.com/linux-amd64"},
		},
	}
	_, err = releaseNoChecksums.FindChecksums()
	if err == nil {
		t.Error("FindChecksums() expected error for release without checksums")
	}
}

func TestUpdaterDownload(t *testing.T) {
	// Create a test binary content
	binaryContent := []byte("fake binary content for testing")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "tunnelmesh-"+runtime.GOOS+"-"+runtime.GOARCH) ||
			strings.HasSuffix(r.URL.Path, "tunnelmesh-"+runtime.GOOS+"-"+runtime.GOARCH+".exe") {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(binaryContent)))
			_, _ = w.Write(binaryContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
	})

	assetName := GetAssetName(runtime.GOOS, runtime.GOARCH)
	asset := Asset{
		Name:        assetName,
		DownloadURL: server.URL + "/" + assetName,
		Size:        int64(len(binaryContent)),
	}

	// Download to temp file
	downloadPath, err := u.Download(&asset, nil)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	defer func() { _ = os.Remove(downloadPath) }()

	// Verify content
	content, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(content) != string(binaryContent) {
		t.Errorf("downloaded content = %q, want %q", content, binaryContent)
	}
}

func TestUpdaterDownloadWithProgress(t *testing.T) {
	binaryContent := []byte("fake binary content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(binaryContent)))
		_, _ = w.Write(binaryContent)
	}))
	defer server.Close()

	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
	})

	asset := Asset{
		Name:        "test-binary",
		DownloadURL: server.URL + "/test-binary",
		Size:        int64(len(binaryContent)),
	}

	var progressCalled bool
	progressFn := func(downloaded, total int64) {
		progressCalled = true
		if total != int64(len(binaryContent)) {
			t.Errorf("total = %d, want %d", total, len(binaryContent))
		}
	}

	downloadPath, err := u.Download(&asset, progressFn)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	defer func() { _ = os.Remove(downloadPath) }()

	if !progressCalled {
		t.Error("progress callback was not called")
	}
}

func TestParseChecksums(t *testing.T) {
	checksumContent := `abc123def456789012345678901234567890123456789012345678901234  tunnelmesh-linux-amd64
def456abc123789012345678901234567890123456789012345678901234  tunnelmesh-darwin-arm64
111222333444555666777888999000aaabbbcccdddeeefffggghhhiii  tunnelmesh-windows-amd64.exe
`

	checksums, err := ParseChecksums(checksumContent)
	if err != nil {
		t.Fatalf("ParseChecksums() error: %v", err)
	}

	expected := map[string]string{
		"tunnelmesh-linux-amd64":       "abc123def456789012345678901234567890123456789012345678901234",
		"tunnelmesh-darwin-arm64":      "def456abc123789012345678901234567890123456789012345678901234",
		"tunnelmesh-windows-amd64.exe": "111222333444555666777888999000aaabbbcccdddeeefffggghhhiii",
	}

	for name, wantHash := range expected {
		gotHash, ok := checksums[name]
		if !ok {
			t.Errorf("checksums[%q] not found", name)
			continue
		}
		if gotHash != wantHash {
			t.Errorf("checksums[%q] = %q, want %q", name, gotHash, wantHash)
		}
	}
}

func TestVerifyChecksum(t *testing.T) {
	// Create a temp file with known content
	content := []byte("test content for checksum verification")
	tmpFile, err := os.CreateTemp("", "checksum-test-*")
	if err != nil {
		t.Fatalf("CreateTemp() error: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.Write(content); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	_ = tmpFile.Close()

	// Calculate expected SHA256
	hash := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(hash[:])

	tests := []struct {
		name    string
		hash    string
		wantErr bool
	}{
		{
			name:    "valid checksum",
			hash:    expectedHash,
			wantErr: false,
		},
		{
			name:    "invalid checksum",
			hash:    "0000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyChecksum(tmpFile.Name(), tt.hash)
			if tt.wantErr && err == nil {
				t.Error("VerifyChecksum() expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("VerifyChecksum() unexpected error: %v", err)
			}
		})
	}
}

func TestUpdaterVerifyDownload(t *testing.T) {
	// Create test content and calculate checksum
	binaryContent := []byte("fake binary content for testing checksum")
	hash := sha256.Sum256(binaryContent)
	checksum := hex.EncodeToString(hash[:])

	checksumFileContent := fmt.Sprintf("%s  tunnelmesh-linux-amd64\n", checksum)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			_, _ = w.Write([]byte(checksumFileContent))
		case strings.HasSuffix(r.URL.Path, "tunnelmesh-linux-amd64"):
			_, _ = w.Write(binaryContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	u := NewUpdater(Config{
		GitHubOwner: "testowner",
		GitHubRepo:  "testrepo",
	})

	release := &ReleaseInfo{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: "tunnelmesh-linux-amd64", DownloadURL: server.URL + "/tunnelmesh-linux-amd64"},
			{Name: "checksums.txt", DownloadURL: server.URL + "/checksums.txt"},
		},
	}

	// Create temp file with binary content
	tmpFile, err := os.CreateTemp("", "update-test-*")
	if err != nil {
		t.Fatalf("CreateTemp() error: %v", err)
	}
	_, _ = tmpFile.Write(binaryContent)
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	// Verify should pass
	err = u.VerifyDownload(tmpFile.Name(), "tunnelmesh-linux-amd64", release)
	if err != nil {
		t.Errorf("VerifyDownload() unexpected error: %v", err)
	}

	// Write wrong content and verify should fail
	wrongFile, _ := os.CreateTemp("", "update-test-wrong-*")
	_, _ = wrongFile.Write([]byte("wrong content"))
	_ = wrongFile.Close()
	defer func() { _ = os.Remove(wrongFile.Name()) }()

	err = u.VerifyDownload(wrongFile.Name(), "tunnelmesh-linux-amd64", release)
	if err == nil {
		t.Error("VerifyDownload() expected error for wrong content")
	}
}

func TestCalculateChecksum(t *testing.T) {
	content := []byte("test content")
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test-file")

	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	expectedHash := sha256.Sum256(content)
	expectedHex := hex.EncodeToString(expectedHash[:])

	gotHex, err := CalculateChecksum(tmpFile)
	if err != nil {
		t.Fatalf("CalculateChecksum() error: %v", err)
	}

	if gotHex != expectedHex {
		t.Errorf("CalculateChecksum() = %q, want %q", gotHex, expectedHex)
	}
}
