package documents

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// ContentLoader loads pre-generated content from disk
type ContentLoader struct {
	dataDir string
	index   *ContentIndex
	rand    *rand.Rand
}

// ContentIndex matches the structure of index.json
type ContentIndex struct {
	GeneratedAt   string                       `json:"generated_at"`
	Generator     string                       `json:"generator"`
	OllamaModel   string                       `json:"ollama_model"`
	Seed          int64                        `json:"seed"`
	DedupTarget   string                       `json:"dedup_target"`
	DocumentTypes map[string]DocumentTypeIndex `json:"document_types"`
	Statistics    IndexStatistics              `json:"statistics"`
}

// DocumentTypeIndex contains metadata about a document type
type DocumentTypeIndex struct {
	Format        string   `json:"format"`
	Files         []string `json:"files"`
	TemplateCount int      `json:"template_count"`
	VersionCount  int      `json:"version_count"`
	Authors       []string `json:"authors"`
	Phases        []int    `json:"phases"`
	TotalFiles    int      `json:"total_files"`
}

// IndexStatistics contains overall statistics
type IndexStatistics struct {
	TotalDocuments      int     `json:"total_documents"`
	TotalSizeBytes      int64   `json:"total_size_bytes"`
	MarkdownFiles       int     `json:"markdown_files"`
	JSONFiles           int     `json:"json_files"`
	EstimatedDedupRatio float64 `json:"estimated_dedup_ratio"`
}

// NewContentLoader creates a loader for the specified scenario
func NewContentLoader(scenario string, seed int64) (*ContentLoader, error) {
	if scenario != "alien_invasion" {
		return nil, fmt.Errorf("unsupported scenario: %s (only alien_invasion supported)", scenario)
	}

	// Determine data directory
	dataDir := "internal/s3bench/data/alien_invasion"

	// Check if directory exists
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("data directory not found: %s (run: make generate-s3bench-data)", dataDir)
	}

	// Load index
	indexPath := filepath.Join(dataDir, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read index.json: %w", err)
	}

	var index ContentIndex
	if err := json.Unmarshal(indexData, &index); err != nil {
		return nil, fmt.Errorf("failed to parse index.json: %w", err)
	}

	log.Info().
		Int("documents", index.Statistics.TotalDocuments).
		Float64("dedup_target", index.Statistics.EstimatedDedupRatio).
		Str("model", index.OllamaModel).
		Msg("Loaded pre-generated content index")

	return &ContentLoader{
		dataDir: dataDir,
		index:   &index,
		rand:    rand.New(rand.NewSource(seed)),
	}, nil
}

// LoadContent loads content for the specified document type and context
func (l *ContentLoader) LoadContent(docType string, ctx story.Context, version int) ([]byte, string, error) {
	// Get document type index
	docIndex, exists := l.index.DocumentTypes[docType]
	if !exists {
		return nil, "", fmt.Errorf("document type not found in index: %s", docType)
	}

	// Extract context
	phase, ok := ctx.Data["Phase"].(int)
	if !ok {
		return nil, "", fmt.Errorf("missing Phase in context")
	}

	author := ctx.Author.ID

	// Get document number for deterministic template selection
	docNum, ok := ctx.Data["DocumentNumber"].(int)
	if !ok {
		// Fallback to random selection if DocumentNumber not provided
		docNum = l.rand.Intn(1000000)
	}

	// Select template deterministically
	templateNum := (docNum % docIndex.TemplateCount) + 1

	// Build file path
	ext := ".md"
	if docIndex.Format == "json" {
		ext = ".json"
	}

	filename := fmt.Sprintf("%s/p%d/%s/t%d_v%d%s",
		docType, phase, author, templateNum, version, ext)

	// Read file
	filePath := filepath.Join(l.dataDir, filename)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read %s: %w", filename, err)
	}

	log.Debug().
		Str("doc_type", docType).
		Int("phase", phase).
		Str("author", author).
		Int("template", templateNum).
		Int("version", version).
		Str("file", filename).
		Int("size", len(content)).
		Msg("Loaded pre-generated content")

	return content, docIndex.Format, nil
}

// Statistics returns statistics about the loaded content
func (l *ContentLoader) Statistics() IndexStatistics {
	return l.index.Statistics
}

// GetDocumentTypes returns the list of available document types
func (l *ContentLoader) GetDocumentTypes() []string {
	types := make([]string, 0, len(l.index.DocumentTypes))
	for docType := range l.index.DocumentTypes {
		types = append(types, docType)
	}
	return types
}

// GetDocumentTypeIndex returns metadata for a specific document type
func (l *ContentLoader) GetDocumentTypeIndex(docType string) (DocumentTypeIndex, bool) {
	idx, exists := l.index.DocumentTypes[docType]
	return idx, exists
}
