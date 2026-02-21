package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/schollz/progressbar/v3"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/llm"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story/scenarios"
)

type Config struct {
	Scenario       string
	OllamaEndpoint string
	OllamaModel    string
	OutputDir      string
	Seed           int64
	Concurrency    int
	DryRun         bool
	Verbose        bool
}

type ContentIndex struct {
	GeneratedAt   time.Time                    `json:"generated_at"`
	Generator     string                       `json:"generator"`
	OllamaModel   string                       `json:"ollama_model"`
	Seed          int64                        `json:"seed"`
	DedupTarget   string                       `json:"dedup_target"`
	DocumentTypes map[string]DocumentTypeIndex `json:"document_types"`
	Statistics    IndexStatistics              `json:"statistics"`
}

type DocumentTypeIndex struct {
	Format        string   `json:"format"`
	Files         []string `json:"files"`
	TemplateCount int      `json:"template_count"`
	VersionCount  int      `json:"version_count"`
	Authors       []string `json:"authors"`
	Phases        []int    `json:"phases"`
	TotalFiles    int      `json:"total_files"`
}

type IndexStatistics struct {
	TotalDocuments      int     `json:"total_documents"`
	TotalSizeBytes      int64   `json:"total_size_bytes"`
	MarkdownFiles       int     `json:"markdown_files"`
	JSONFiles           int     `json:"json_files"`
	EstimatedDedupRatio float64 `json:"estimated_dedup_ratio"`
}

type GenerationTask struct {
	DocType     string
	Phase       int
	Author      string
	TemplateNum int
	VersionNum  int
	Context     llm.PromptContext
	OutputPath  string
}

func main() {
	config := parseFlags()
	setupLogging(config.Verbose)

	if err := run(config); err != nil {
		log.Fatal().Err(err).Msg("Content generation failed")
	}
}

func parseFlags() Config {
	config := Config{}

	flag.StringVar(&config.Scenario, "scenario", "alien_invasion", "Scenario to generate content for")
	flag.StringVar(&config.OllamaEndpoint, "ollama-endpoint", "http://honker:11434", "Ollama API endpoint")
	flag.StringVar(&config.OllamaModel, "ollama-model", "gpt-oss:120b", "Ollama model name")
	flag.StringVar(&config.OutputDir, "output", "internal/s3bench/data/alien_invasion", "Output directory")
	flag.Int64Var(&config.Seed, "seed", 42, "Random seed for reproducibility")
	flag.IntVar(&config.Concurrency, "concurrency", 10, "Number of parallel LLM requests")
	flag.BoolVar(&config.DryRun, "dry-run", false, "Show what would be generated without calling Ollama")
	flag.BoolVar(&config.Verbose, "verbose", false, "Enable verbose logging")

	flag.Parse()
	return config
}

func setupLogging(verbose bool) {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	if verbose {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

func run(config Config) error {
	ctx := context.Background()

	// Load scenario
	log.Info().Str("scenario", config.Scenario).Msg("Loading scenario")
	st, err := loadScenario(config.Scenario)
	if err != nil {
		return fmt.Errorf("failed to load scenario: %w", err)
	}

	// Initialize Ollama client
	var client llm.Client
	if !config.DryRun {
		log.Info().
			Str("endpoint", config.OllamaEndpoint).
			Str("model", config.OllamaModel).
			Msg("Connecting to Ollama")

		ollamaConfig := llm.Config{
			Endpoint:   config.OllamaEndpoint,
			Model:      config.OllamaModel,
			Timeout:    120 * time.Second,
			MaxRetries: 3,
		}

		ollamaClient, err := llm.NewOllamaClient(ollamaConfig)
		if err != nil {
			return fmt.Errorf("failed to create Ollama client: %w", err)
		}

		if !ollamaClient.IsAvailable(ctx) {
			return fmt.Errorf("ollama not available at %s or model %s not found", config.OllamaEndpoint, config.OllamaModel)
		}

		log.Info().Msg("✓ Ollama connection successful")
		client = ollamaClient
	} else {
		log.Info().Msg("Dry run mode - skipping Ollama connection")
	}

	// Build generation tasks
	log.Info().Msg("Building generation task list")
	tasks := buildGenerationTasks(st, config)
	log.Info().Int("total_tasks", len(tasks)).Msg("Generation tasks created")

	if config.DryRun {
		return printDryRunSummary(tasks, config)
	}

	// Create output directory
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate content
	startTime := time.Now()
	log.Info().Msg("Starting content generation")

	generatedFiles, totalBytes, err := generateContent(ctx, client, tasks, config)
	if err != nil {
		return fmt.Errorf("content generation failed: %w", err)
	}

	duration := time.Since(startTime)

	// Create index
	log.Info().Msg("Creating content index")
	index := createIndex(generatedFiles, config, st, totalBytes)

	indexPath := filepath.Join(config.OutputDir, "index.json")
	if err := writeJSON(indexPath, index); err != nil {
		return fmt.Errorf("failed to write index: %w", err)
	}

	// Create README
	readmePath := filepath.Join(config.OutputDir, "README.md")
	if err := writeREADME(readmePath, config); err != nil {
		return fmt.Errorf("failed to write README: %w", err)
	}

	// Print summary
	printSummary(index, duration)

	return nil
}

func loadScenario(name string) (story.Story, error) {
	if name != "alien_invasion" {
		return nil, fmt.Errorf("unsupported scenario: %s (only alien_invasion supported)", name)
	}
	return &scenarios.AlienInvasion{}, nil
}

func buildGenerationTasks(st story.Story, config Config) []GenerationTask {
	var tasks []GenerationTask

	const templatesPerType = 10
	maxVersions := 4

	// Derive phases (alien_invasion has 3 phases: 0-24h, 24-48h, 48-72h)
	phases := []int{1, 2, 3}

	for _, rule := range st.DocumentRules() {
		// Determine versions
		versions := 1
		if rule.Versions > 1 {
			versions = maxVersions
		}

		// For each phase
		for _, phaseNum := range phases {
			// For each author
			for _, authorID := range rule.Authors {
				// Find author details
				authorObj := findAuthor(st, authorID)
				if authorObj == nil {
					continue
				}

				// For each template
				for templateNum := 1; templateNum <= templatesPerType; templateNum++ {
					// For each version
					for versionNum := 1; versionNum <= versions; versionNum++ {
						// Determine format
						format := selectFormat(rule.Type)

						// Build context
						phaseName := getPhaseName(phaseNum)
						ctx := llm.PromptContext{
							DocType:        rule.Type,
							Format:         format,
							Author:         authorID,
							AuthorRole:     authorObj.Role,
							Phase:          phaseNum,
							PhaseName:      phaseName,
							TimelineEvent:  getPhaseEvent(st, phaseNum),
							HoursIntoEvent: (phaseNum - 1) * 24,
							ThreatLevel:    phaseNum, // Escalates with phase
							CasualtyCount:  1000 * phaseNum,
							TargetLength:   getTargetLength(rule, format),
							Seed:           config.Seed,
							TemplateNum:    templateNum,
							VersionNum:     versionNum,
						}

						// Build output path
						ext := "." + format
						if format == "markdown" {
							ext = ".md"
						}
						filename := fmt.Sprintf("t%d_v%d%s", templateNum, versionNum, ext)
						outputPath := filepath.Join(
							config.OutputDir,
							rule.Type,
							fmt.Sprintf("p%d", phaseNum),
							authorID,
							filename,
						)

						tasks = append(tasks, GenerationTask{
							DocType:     rule.Type,
							Phase:       phaseNum,
							Author:      authorID,
							TemplateNum: templateNum,
							VersionNum:  versionNum,
							Context:     ctx,
							OutputPath:  outputPath,
						})
					}
				}
			}
		}
	}

	return tasks
}

func selectFormat(docType string) string {
	// Determine format based on document type
	if strings.Contains(docType, "list") ||
		strings.Contains(docType, "data") ||
		docType == "sensor_readings" {
		return "json"
	}
	return "markdown"
}

func getPhaseName(phase int) string {
	switch phase {
	case 1:
		return "FIRST CONTACT"
	case 2:
		return "INVASION"
	case 3:
		return "RESISTANCE"
	default:
		return "UNKNOWN"
	}
}

func getPhaseEvent(st story.Story, phase int) string {
	timeline := st.Timeline()
	// Find first event in this phase
	phaseStart := time.Duration(phase-1) * 24 * time.Hour
	phaseEnd := time.Duration(phase) * 24 * time.Hour

	for _, event := range timeline {
		if event.Time >= phaseStart && event.Time < phaseEnd {
			return event.Description
		}
	}
	return fmt.Sprintf("Phase %d events", phase)
}

func findAuthor(st story.Story, authorID string) *story.Character {
	for _, char := range st.Characters() {
		if char.ID == authorID {
			c := char // Copy to avoid pointer to loop variable
			return &c
		}
	}
	return nil
}

func getTargetLength(rule story.DocumentRule, format string) string {
	minSize := rule.SizeRange[0]
	maxSize := rule.SizeRange[1]

	if format == "json" {
		// JSON arrays - describe entry count
		switch {
		case minSize > 100000:
			return "1000-2000 entries"
		case minSize > 10000:
			return "100-500 entries"
		default:
			return "50-100 entries"
		}
	}
	// Markdown - describe word count
	switch {
	case maxSize > 50000:
		return "3000-5000 words"
	case maxSize > 10000:
		return "1000-2000 words"
	default:
		return "500-1000 words"
	}
}

func generateContent(ctx context.Context, client llm.Client, tasks []GenerationTask, config Config) (map[string][]string, int64, error) {
	promptBuilder := llm.NewPromptBuilder()

	// Track generated files by document type
	generatedFiles := make(map[string][]string)
	var filesMu sync.Mutex

	// Track errors
	var errors []error
	var errorsMu sync.Mutex

	// Track total bytes
	var totalBytes int64
	var bytesMu sync.Mutex

	// Progress bar
	bar := progressbar.NewOptions(len(tasks),
		progressbar.OptionSetDescription("Generating content"),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionSetRenderBlankState(true),
	)

	// Worker pool
	taskChan := make(chan GenerationTask, config.Concurrency)
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < config.Concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for task := range taskChan {
				// Build prompt
				req := promptBuilder.BuildPrompt(task.Context)

				// Generate content
				content, err := client.Generate(ctx, req)
				if err != nil {
					log.Error().
						Err(err).
						Str("doc_type", task.DocType).
						Int("phase", task.Phase).
						Str("author", task.Author).
						Int("template", task.TemplateNum).
						Int("version", task.VersionNum).
						Msg("Failed to generate content")

					errorsMu.Lock()
					errors = append(errors, fmt.Errorf("%s p%d %s t%d_v%d: %w",
						task.DocType, task.Phase, task.Author, task.TemplateNum, task.VersionNum, err))
					errorsMu.Unlock()

					_ = bar.Add(1)
					continue
				}

				// Create directory
				dir := filepath.Dir(task.OutputPath)
				if err := os.MkdirAll(dir, 0755); err != nil {
					log.Error().Err(err).Str("dir", dir).Msg("Failed to create directory")
					errorsMu.Lock()
					errors = append(errors, err)
					errorsMu.Unlock()
					_ = bar.Add(1)
					continue
				}

				// Write file
				if err := os.WriteFile(task.OutputPath, []byte(content), 0644); err != nil {
					log.Error().Err(err).Str("path", task.OutputPath).Msg("Failed to write file")
					errorsMu.Lock()
					errors = append(errors, err)
					errorsMu.Unlock()
					_ = bar.Add(1)
					continue
				}

				// Track file
				relPath, _ := filepath.Rel(config.OutputDir, task.OutputPath)
				filesMu.Lock()
				generatedFiles[task.DocType] = append(generatedFiles[task.DocType], relPath)
				filesMu.Unlock()

				// Track size
				bytesMu.Lock()
				totalBytes += int64(len(content))
				bytesMu.Unlock()

				_ = bar.Add(1)
			}
		}(i)
	}

	// Send tasks
	for _, task := range tasks {
		taskChan <- task
	}
	close(taskChan)

	// Wait for completion
	wg.Wait()
	_ = bar.Finish()

	if len(errors) > 0 {
		log.Warn().Int("error_count", len(errors)).Msg("Some content generation failed")
		// Write error log
		errorLog := filepath.Join(config.OutputDir, "generation_errors.log")
		f, _ := os.Create(errorLog)
		if f != nil {
			defer func() { _ = f.Close() }()
			for _, err := range errors {
				_, _ = fmt.Fprintf(f, "%v\n", err)
			}
			log.Info().Str("path", errorLog).Msg("Errors written to log file")
		}
	}

	return generatedFiles, totalBytes, nil
}

func createIndex(generatedFiles map[string][]string, config Config, st story.Story, totalBytes int64) ContentIndex {
	index := ContentIndex{
		GeneratedAt:   time.Now(),
		Generator:     "generate-s3bench-data",
		OllamaModel:   config.OllamaModel,
		Seed:          config.Seed,
		DedupTarget:   "3-5x",
		DocumentTypes: make(map[string]DocumentTypeIndex),
		Statistics: IndexStatistics{
			EstimatedDedupRatio: 3.8, // Target estimate
		},
	}

	for docType, files := range generatedFiles {
		// Find rule
		var rule *story.DocumentRule
		for _, r := range st.DocumentRules() {
			if r.Type == docType {
				rCopy := r
				rule = &rCopy
				break
			}
		}
		if rule == nil {
			continue
		}

		// Determine format
		format := selectFormat(docType)

		// Get unique authors and phases
		authors := rule.Authors
		phases := []int{1, 2, 3}

		// Count versions
		versions := 1
		if rule.Versions > 1 {
			versions = 4
		}

		docIndex := DocumentTypeIndex{
			Format:        format,
			Files:         files,
			TemplateCount: 10,
			VersionCount:  versions,
			Authors:       authors,
			Phases:        phases,
			TotalFiles:    len(files),
		}

		index.DocumentTypes[docType] = docIndex

		// Update statistics
		index.Statistics.TotalDocuments += len(files)
		if format == "json" {
			index.Statistics.JSONFiles += len(files)
		} else {
			index.Statistics.MarkdownFiles += len(files)
		}
	}

	index.Statistics.TotalSizeBytes = totalBytes

	return index
}

func writeJSON(path string, data interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

func writeREADME(path string, config Config) error {
	content := fmt.Sprintf(`# S3Bench Generated Content

This directory contains AI-generated content for the %s scenario.

## Generation Details

- **Generated:** %s
- **Model:** %s (via %s)
- **Seed:** %d
- **Target Dedup Ratio:** 3-5x

## How to Regenerate

To regenerate this content locally:

`+"```bash"+`
make generate-s3bench-data

# Or directly:
go run cmd/generate-s3bench-data/main.go \
    --scenario %s \
    --ollama-endpoint %s \
    --ollama-model %s \
    --output %s \
    --seed %d \
    --concurrency 10
`+"```"+`

## Content Structure

- Each document type has subdirectories by phase (p1, p2, p3) and author
- Files are named t{template}_v{version}.{ext} where:
  - template: 1-10 (different template variations)
  - version: 1-4 (version revisions for documents that get updated)
  - ext: md (markdown) or json

## Deduplication Strategy

Content is designed for 3-5x deduplication via:
- **10%% shared boilerplate** (headers, footers, classification banners)
- **60%% template sections** (reusable paragraphs with variable substitution)
- **30%% unique narrative** (context-specific content)

This mimics real-world document patterns where templates and boilerplate are common.

## Not Committed to Git

This directory is .gitignore'd because:
- Total size: ~200+ MB
- Generated content (reproducible)
- Requires Ollama access to regenerate
- Falls back to lorem ipsum when unavailable
`,
		config.Scenario,
		time.Now().Format(time.RFC3339),
		config.OllamaModel,
		config.OllamaEndpoint,
		config.Seed,
		config.Scenario,
		config.OllamaEndpoint,
		config.OllamaModel,
		config.OutputDir,
		config.Seed,
	)

	return os.WriteFile(path, []byte(content), 0644)
}

func printSummary(index ContentIndex, duration time.Duration) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("  Content Generation Complete")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()
	fmt.Printf("✓ Generated %d documents in %s\n", index.Statistics.TotalDocuments, duration.Round(time.Second))
	fmt.Printf("✓ Markdown: %d files\n", index.Statistics.MarkdownFiles)
	fmt.Printf("✓ JSON: %d files\n", index.Statistics.JSONFiles)
	fmt.Printf("✓ Total size: %s\n", formatBytes(index.Statistics.TotalSizeBytes))
	fmt.Printf("✓ Estimated dedup ratio: %.1fx\n", index.Statistics.EstimatedDedupRatio)
	fmt.Println()
	fmt.Println("Index written to: index.json")
	fmt.Println("README written to: README.md")
	fmt.Println()
}

func printDryRunSummary(tasks []GenerationTask, config Config) error {
	// Count by doc type
	typeCount := make(map[string]int)
	for _, task := range tasks {
		typeCount[task.DocType]++
	}

	fmt.Println()
	fmt.Println("Dry Run Summary")
	fmt.Println("===============")
	fmt.Println()
	fmt.Printf("Scenario: %s\n", config.Scenario)
	fmt.Printf("Total tasks: %d\n", len(tasks))
	fmt.Println()
	fmt.Println("Documents by type:")
	for docType, count := range typeCount {
		fmt.Printf("  %-30s %d files\n", docType, count)
	}
	fmt.Println()
	fmt.Println("Run without --dry-run to generate content")
	fmt.Println()

	return nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
