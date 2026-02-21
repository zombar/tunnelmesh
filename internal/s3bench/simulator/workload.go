package simulator

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/documents"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// WorkloadTask represents a single document operation to perform.
type WorkloadTask struct {
	// Task identification
	TaskID    string        // Unique task identifier
	StoryTime time.Duration // When this happens in story time
	RealTime  time.Duration // When this happens in real time (scaled)

	// Document info
	DocType     string // Document type (e.g., "battle_report")
	DocNumber   int    // Sequential number for this doc type
	Filename    string // Filename (e.g., "battle_report_34fe34w.md")
	Content     []byte // Generated document content
	ContentType string // MIME type

	// Story context
	Author  story.Character // Character authoring this document
	Phase   int             // Story phase (1, 2, 3)
	Context story.Context   // Full story context

	// Operation type
	Operation  string // "upload", "update", "delete", "download"
	VersionNum int    // For updates: version number

	// File share info
	FileShare string // Target file share

	// Metadata
	ExpiresAt *time.Time        // Document expiration (if any)
	Tags      map[string]string // Document tags
}

// WorkloadGenerator generates document operation tasks based on story rules.
type WorkloadGenerator struct {
	story     story.Story
	generator *documents.Generator
	timeScale float64

	// Tracking
	docCounts map[string]int // Count per doc type
	nextID    int            // Next task ID
}

// NewWorkloadGenerator creates a new workload generator.
func NewWorkloadGenerator(s story.Story, timeScale float64, genOpts ...documents.GeneratorOption) *WorkloadGenerator {
	return &WorkloadGenerator{
		story:     s,
		generator: documents.NewGenerator(s, genOpts...),
		timeScale: timeScale,
		docCounts: make(map[string]int),
	}
}

// GenerateWorkload generates all tasks for the entire scenario.
func (w *WorkloadGenerator) GenerateWorkload(ctx context.Context) ([]WorkloadTask, error) {
	var tasks []WorkloadTask

	duration := w.story.Duration()
	rules := w.story.DocumentRules()
	characters := w.story.Characters()
	departments := w.story.Departments()

	// Build character map for lookup
	charMap := make(map[string]story.Character)
	for _, char := range characters {
		charMap[char.ID] = char
	}

	// Build department map for lookup
	deptMap := make(map[string]story.Department)
	for _, dept := range departments {
		deptMap[dept.ID] = dept
	}

	// For each document rule, generate documents at regular intervals
	for _, rule := range rules {
		// Calculate how many documents to generate
		count := int(duration / rule.Frequency)

		for i := 0; i < count; i++ {
			// Determine story time for this document
			storyTime := rule.Frequency * time.Duration(i)
			if storyTime >= duration {
				break
			}

			// Choose an author from the rule's author list
			authorID := rule.Authors[i%len(rule.Authors)]
			author, ok := charMap[authorID]
			if !ok {
				return nil, fmt.Errorf("author %s not found for document type %s", authorID, rule.Type)
			}

			// Skip if author hasn't joined yet or has left
			if storyTime < author.JoinTime {
				continue
			}
			if author.LeaveTime > 0 && storyTime >= author.LeaveTime {
				continue
			}

			// Increment document count for this type
			w.docCounts[rule.Type]++
			docNum := w.docCounts[rule.Type]

			// Create story context
			ctx := story.Context{
				Timestamp:    time.Now().Add(storyTime),
				StoryElapsed: storyTime,
				Author:       author,
				Data: map[string]interface{}{
					"DocumentNumber": docNum,
					"Rule":           rule,
				},
			}

			// Generate document content
			content, format, err := w.generator.Generate(rule, ctx)
			if err != nil {
				return nil, fmt.Errorf("generating document %s #%d: %w", rule.Type, docNum, err)
			}

			// Store format in context for version consistency
			ctx.Data["Format"] = format

			// Generate filename with random suffix and correct extension
			var ext string
			var contentType string
			switch format {
			case "markdown":
				ext = ".md"
				contentType = "text/markdown"
			case "json":
				ext = ".json"
				contentType = "application/json"
			default:
				ext = ".txt"
				contentType = "text/plain"
			}
			filename := fmt.Sprintf("%s_%s%s", rule.Type, generateRandomID(8), ext)

			// Determine file share (use explicit FileShare or author's department)
			fileShare := rule.FileShare
			if fileShare == "" {
				// Default to author's department
				dept, ok := deptMap[author.Department]
				if !ok {
					return nil, fmt.Errorf("department %s not found for author %s", author.Department, author.ID)
				}
				fileShare = dept.FileShare
			}

			// Calculate expiration if specified
			var expiresAt *time.Time
			if rule.ExpiresIn > 0 {
				expiry := time.Now().Add(storyTime + rule.ExpiresIn)
				expiresAt = &expiry
			}

			// Create upload task
			task := WorkloadTask{
				TaskID:      w.nextTaskID(),
				StoryTime:   storyTime,
				RealTime:    story.ScaledDuration(storyTime, w.timeScale),
				DocType:     rule.Type,
				DocNumber:   docNum,
				Filename:    filename,
				Content:     content,
				ContentType: contentType,
				Author:      author,
				Phase:       documents.GetPhase(storyTime),
				Context:     ctx,
				Operation:   "upload",
				VersionNum:  1,
				FileShare:   fileShare,
				ExpiresAt:   expiresAt,
				Tags: map[string]string{
					"doc_type":  rule.Type,
					"author":    author.Name,
					"phase":     fmt.Sprintf("%d", documents.GetPhase(storyTime)),
					"clearance": fmt.Sprintf("%d", author.Clearance),
				},
			}
			tasks = append(tasks, task)

			// Generate version updates if specified
			for v := 2; v <= rule.Versions; v++ {
				// Version updates happen at intervals after initial upload
				versionTime := storyTime + (rule.Frequency/time.Duration(rule.Versions))*time.Duration(v-1)
				if versionTime >= duration {
					break
				}

				// Check author still active
				if author.LeaveTime > 0 && versionTime >= author.LeaveTime {
					break
				}

				// Generate updated content (preserve format from original)
				versionCtx := ctx
				versionCtx.Timestamp = time.Now().Add(versionTime)
				versionCtx.StoryElapsed = versionTime
				versionCtx.Data["DocumentNumber"] = docNum
				versionCtx.Data["Version"] = v
				// Format is already in ctx.Data["Format"] and will be reused

				versionContent, _, err := w.generator.Generate(rule, versionCtx)
				if err != nil {
					return nil, fmt.Errorf("generating document version %s #%d v%d: %w", rule.Type, docNum, v, err)
				}

				versionTask := WorkloadTask{
					TaskID:      w.nextTaskID(),
					StoryTime:   versionTime,
					RealTime:    story.ScaledDuration(versionTime, w.timeScale),
					DocType:     rule.Type,
					DocNumber:   docNum,
					Filename:    filename, // Same filename, new version
					Content:     versionContent,
					ContentType: contentType, // Use same content type as original
					Author:      author,
					Phase:       documents.GetPhase(versionTime),
					Context:     versionCtx,
					Operation:   "update",
					VersionNum:  v,
					FileShare:   fileShare,
					ExpiresAt:   expiresAt,
					Tags: map[string]string{
						"doc_type":  rule.Type,
						"author":    author.Name,
						"phase":     fmt.Sprintf("%d", documents.GetPhase(versionTime)),
						"clearance": fmt.Sprintf("%d", author.Clearance),
						"version":   fmt.Sprintf("%d", v),
					},
				}
				tasks = append(tasks, versionTask)
			}

			// Generate deletion task if specified
			if rule.DeleteAfter > 0 {
				deleteTime := storyTime + rule.DeleteAfter
				if deleteTime < duration {
					deleteTask := WorkloadTask{
						TaskID:    w.nextTaskID(),
						StoryTime: deleteTime,
						RealTime:  story.ScaledDuration(deleteTime, w.timeScale),
						DocType:   rule.Type,
						DocNumber: docNum,
						Filename:  filename,
						Author:    author,
						Phase:     documents.GetPhase(deleteTime),
						Operation: "delete",
						FileShare: fileShare,
					}
					tasks = append(tasks, deleteTask)
				}
			}
		}
	}

	// Generate download operations (20% of uploads get downloaded)
	downloadTasks := w.generateDownloadTasks(tasks)
	tasks = append(tasks, downloadTasks...)

	// Sort tasks by real time
	sortTasksByTime(tasks)

	return tasks, nil
}

// generateDownloadTasks creates download tasks for some uploaded documents.
func (w *WorkloadGenerator) generateDownloadTasks(uploadTasks []WorkloadTask) []WorkloadTask {
	var downloads []WorkloadTask

	// Track uploaded documents by filename and their scheduled delete times
	uploadedDocs := make(map[string]WorkloadTask)
	deleteTimes := make(map[string]time.Duration)
	for _, task := range uploadTasks {
		if task.Operation == "upload" || task.Operation == "update" {
			uploadedDocs[task.Filename] = task
		}
		if task.Operation == "delete" {
			deleteTimes[task.Filename] = task.StoryTime
		}
	}

	// Generate downloads for ~20% of uploaded documents
	for filename, uploadTask := range uploadedDocs {
		// 20% chance to download this document
		if rand.Intn(100) < 20 {
			// Determine the deadline: either the delete time or end of story
			deadline := w.story.Duration()
			if deleteTime, hasDelete := deleteTimes[filename]; hasDelete {
				deadline = deleteTime
			}

			// Safety buffer: at least 2 seconds of real time, minimum 1 story-minute.
			// At high time scales (e.g. 1000x), a 1-minute story buffer is only 60ms
			// real time, which is not enough for concurrent HTTP requests to complete.
			safetyBuffer := time.Duration(float64(2*time.Second) * w.timeScale)
			if safetyBuffer < time.Minute {
				safetyBuffer = time.Minute
			}

			if deadline-uploadTask.StoryTime < safetyBuffer {
				continue
			}

			// Download between upload time and deadline
			window := deadline - uploadTask.StoryTime - safetyBuffer
			downloadTime := uploadTask.StoryTime + time.Duration(rand.Float64()*float64(window))

			downloadTask := WorkloadTask{
				TaskID:      w.nextTaskID(),
				StoryTime:   downloadTime,
				RealTime:    story.ScaledDuration(downloadTime, w.timeScale),
				DocType:     uploadTask.DocType,
				DocNumber:   uploadTask.DocNumber,
				Filename:    filename,
				ContentType: uploadTask.ContentType,
				Author:      uploadTask.Author,
				Phase:       uploadTask.Phase,
				Context:     uploadTask.Context,
				Operation:   "download",
				FileShare:   uploadTask.FileShare,
			}
			downloads = append(downloads, downloadTask)
		}
	}

	return downloads
}

// nextTaskID generates a unique task ID.
func (w *WorkloadGenerator) nextTaskID() string {
	w.nextID++
	return fmt.Sprintf("task_%06d", w.nextID)
}

// generateRandomID generates a random alphanumeric ID of specified length.
func generateRandomID(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// sortTasksByTime sorts tasks by real time (in-place).
func sortTasksByTime(tasks []WorkloadTask) {
	// Sort by real time using efficient O(n log n) algorithm
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].RealTime < tasks[j].RealTime
	})
}
