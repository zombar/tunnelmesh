package documents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

func TestNewContentLoader(t *testing.T) {
	tests := []struct {
		name      string
		scenario  string
		setupFunc func(t *testing.T) string // Returns temp dir path
		wantErr   bool
	}{
		{
			name:     "unsupported scenario",
			scenario: "unknown_scenario",
			setupFunc: func(t *testing.T) string {
				return ""
			},
			wantErr: true,
		},
		{
			name:     "missing data directory",
			scenario: "alien_invasion",
			setupFunc: func(t *testing.T) string {
				// Create temp dir but don't create data
				tmpDir := t.TempDir()
				return tmpDir
			},
			wantErr: true,
		},
		{
			name:     "missing index.json",
			scenario: "alien_invasion",
			setupFunc: func(t *testing.T) string {
				// Create data dir but no index
				tmpDir := t.TempDir()
				dataDir := filepath.Join(tmpDir, "internal/s3bench/data/alien_invasion")
				if err := os.MkdirAll(dataDir, 0755); err != nil {
					t.Fatal(err)
				}
				return tmpDir
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc(t)
			}

			loader, err := NewContentLoader(tt.scenario, 42)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewContentLoader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && loader == nil {
				t.Error("NewContentLoader() returned nil loader")
			}
		})
	}
}

func TestContentLoader_LoadContent(t *testing.T) {
	// Create a mock data directory structure
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "internal/s3bench/data/alien_invasion")

	// Create directory structure
	docTypeDir := filepath.Join(dataDir, "battle_report/p1/commander")
	if err := os.MkdirAll(docTypeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test files for templates 1-5
	testContent := []byte("# Battle Report\n\nTactical situation summary...\n")
	for i := 1; i <= 5; i++ {
		testFile := filepath.Join(docTypeDir, fmt.Sprintf("t%d_v1.md", i))
		if err := os.WriteFile(testFile, testContent, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create index.json
	index := ContentIndex{
		GeneratedAt: "2026-02-21T00:00:00Z",
		Generator:   "test",
		OllamaModel: "test-model",
		Seed:        42,
		DedupTarget: "3-5x",
		DocumentTypes: map[string]DocumentTypeIndex{
			"battle_report": {
				Format:        "markdown",
				Files:         []string{"battle_report/p1/commander/t1_v1.md"},
				TemplateCount: 10,
				VersionCount:  4,
				Authors:       []string{"commander"},
				Phases:        []int{1, 2, 3},
				TotalFiles:    1,
			},
		},
		Statistics: IndexStatistics{
			TotalDocuments:      1,
			TotalSizeBytes:      int64(len(testContent)),
			MarkdownFiles:       1,
			JSONFiles:           0,
			EstimatedDedupRatio: 3.8,
		},
	}

	indexData, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}

	indexPath := filepath.Join(dataDir, "index.json")
	if err := os.WriteFile(indexPath, indexData, 0644); err != nil {
		t.Fatal(err)
	}

	// Change to temp dir so relative paths work
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	_ = os.Chdir(tmpDir)

	// Create loader
	loader, err := NewContentLoader("alien_invasion", 42)
	if err != nil {
		t.Fatalf("NewContentLoader() failed: %v", err)
	}

	tests := []struct {
		name        string
		docType     string
		ctx         story.Context
		version     int
		wantErr     bool
		wantContent string
		wantFormat  string
	}{
		{
			name:    "load existing content",
			docType: "battle_report",
			ctx: story.Context{
				Author: story.Character{ID: "commander"},
				Data: map[string]interface{}{
					"Phase":          1,
					"DocumentNumber": 1,
				},
			},
			version:     1,
			wantErr:     false,
			wantContent: "# Battle Report",
			wantFormat:  "markdown",
		},
		{
			name:    "document type not found",
			docType: "unknown_type",
			ctx: story.Context{
				Author: story.Character{ID: "commander"},
				Data: map[string]interface{}{
					"Phase":          1,
					"DocumentNumber": 1,
				},
			},
			version: 1,
			wantErr: true,
		},
		{
			name:    "missing phase in context",
			docType: "battle_report",
			ctx: story.Context{
				Author: story.Character{ID: "commander"},
				Data: map[string]interface{}{
					"DocumentNumber": 1,
				},
			},
			version: 1,
			wantErr: true,
		},
		{
			name:    "file not found",
			docType: "battle_report",
			ctx: story.Context{
				Author: story.Character{ID: "commander"},
				Data: map[string]interface{}{
					"Phase":          2, // Phase 2 doesn't exist
					"DocumentNumber": 1,
				},
			},
			version: 1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, format, err := loader.LoadContent(tt.docType, tt.ctx, tt.version)

			if (err != nil) != tt.wantErr {
				t.Errorf("LoadContent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if format != tt.wantFormat {
					t.Errorf("LoadContent() format = %v, want %v", format, tt.wantFormat)
				}

				contentStr := string(content)
				if tt.wantContent != "" && len(contentStr) > 0 {
					if contentStr[:len(tt.wantContent)] != tt.wantContent {
						t.Errorf("LoadContent() content doesn't start with expected string")
					}
				}
			}
		})
	}
}

func TestContentLoader_DeterministicTemplateSelection(t *testing.T) {
	// Create a mock data directory structure
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "internal/s3bench/data/alien_invasion")

	// Create directory structure with multiple templates
	docTypeDir := filepath.Join(dataDir, "battle_report/p1/commander")
	if err := os.MkdirAll(docTypeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create multiple template files
	for i := 1; i <= 5; i++ {
		content := []byte(fmt.Sprintf("Template %d content", i))
		filename := fmt.Sprintf("t%d_v1.md", i)
		path := filepath.Join(docTypeDir, filename)
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create index.json
	index := ContentIndex{
		GeneratedAt: "2026-02-21T00:00:00Z",
		Generator:   "test",
		DocumentTypes: map[string]DocumentTypeIndex{
			"battle_report": {
				Format:        "markdown",
				TemplateCount: 5,
				VersionCount:  1,
				Authors:       []string{"commander"},
				Phases:        []int{1},
			},
		},
	}

	indexData, _ := json.Marshal(index)
	indexPath := filepath.Join(dataDir, "index.json")
	_ = os.WriteFile(indexPath, indexData, 0644)

	// Change to temp dir
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	_ = os.Chdir(tmpDir)

	loader, err := NewContentLoader("alien_invasion", 42)
	if err != nil {
		t.Fatalf("NewContentLoader() failed: %v", err)
	}

	// Test that same DocumentNumber always selects same template
	ctx := story.Context{
		Author: story.Character{ID: "commander"},
		Data: map[string]interface{}{
			"Phase":          1,
			"DocumentNumber": 7, // Should select template (7 % 5) + 1 = 3
		},
	}

	// Load twice with same document number
	content1, _, _ := loader.LoadContent("battle_report", ctx, 1)
	content2, _, _ := loader.LoadContent("battle_report", ctx, 1)

	if string(content1) != string(content2) {
		t.Error("LoadContent() should return same content for same DocumentNumber")
	}

	// Verify it's template 3
	if string(content1) != "Template 3 content" {
		t.Errorf("Expected template 3, got: %s", string(content1))
	}
}

func TestContentLoader_Statistics(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "internal/s3bench/data/alien_invasion")
	_ = os.MkdirAll(dataDir, 0755)

	index := ContentIndex{
		Statistics: IndexStatistics{
			TotalDocuments:      2847,
			TotalSizeBytes:      222298112,
			MarkdownFiles:       1708,
			JSONFiles:           1139,
			EstimatedDedupRatio: 3.8,
		},
	}

	indexData, _ := json.Marshal(index)
	_ = os.WriteFile(filepath.Join(dataDir, "index.json"), indexData, 0644)

	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	_ = os.Chdir(tmpDir)

	loader, err := NewContentLoader("alien_invasion", 42)
	if err != nil {
		t.Fatalf("NewContentLoader() failed: %v", err)
	}

	stats := loader.Statistics()

	if stats.TotalDocuments != 2847 {
		t.Errorf("Statistics() TotalDocuments = %d, want 2847", stats.TotalDocuments)
	}

	if stats.EstimatedDedupRatio != 3.8 {
		t.Errorf("Statistics() EstimatedDedupRatio = %f, want 3.8", stats.EstimatedDedupRatio)
	}
}
