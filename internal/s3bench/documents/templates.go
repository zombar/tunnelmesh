// Package documents provides document generation for story-driven stress testing.
package documents

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"math"
	"strings"
	"text/template"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// Generator generates documents based on story context.
type Generator struct {
	story     story.Story
	docCounts map[string]int // Track document counts per type
}

// NewGenerator creates a new document generator for the given story.
func NewGenerator(s story.Story) *Generator {
	return &Generator{
		story:     s,
		docCounts: make(map[string]int),
	}
}

// Generate creates a document based on the rule and context.
func (g *Generator) Generate(rule story.DocumentRule, ctx story.Context) ([]byte, error) {
	g.docCounts[rule.Type]++

	// Add document count to context
	if ctx.Data == nil {
		ctx.Data = make(map[string]interface{})
	}
	ctx.Data["DocumentNumber"] = g.docCounts[rule.Type]
	ctx.Data["Rule"] = rule

	// Select generator function based on document type
	var content []byte
	var err error

	switch rule.Type {
	case "battle_report":
		content, err = GenerateBattleReport(ctx)
	case "sitrep":
		content, err = GenerateSITREP(ctx)
	case "autopsy":
		content, err = GenerateAutopsy(ctx)
	case "scientific_analysis":
		content, err = GenerateScientificAnalysis(ctx)
	case "traffic_report":
		content, err = GenerateTrafficReport(ctx)
	case "council_meeting":
		content, err = GenerateCouncilMeeting(ctx)
	case "telephone":
		content, err = GenerateTelephone(ctx)
	case "im_transcript":
		content, err = GenerateIMTranscript(ctx)
	case "casualty_list":
		content, err = GenerateCasualtyList(ctx)
	case "supply_manifest":
		content, err = GenerateSupplyManifest(ctx)
	case "intel_brief":
		content, err = GenerateIntelBrief(ctx)
	case "radio_log":
		content, err = GenerateRadioLog(ctx)
	case "press_release":
		content, err = GeneratePressRelease(ctx)
	case "private_diary":
		content, err = GeneratePrivateDiary(ctx)
	case "hospital_records":
		content, err = GenerateHospitalRecords(ctx)
	case "lab_results":
		content, err = GenerateLabResults(ctx)
	case "field_notes":
		content, err = GenerateFieldNotes(ctx)
	case "drone_footage":
		content, err = GenerateDroneFootage(ctx)
	case "intercepted_comms":
		content, err = GenerateInterceptedComms(ctx)
	case "evacuation_order":
		content, err = GenerateEvacuationOrder(ctx)
	case "status_update":
		content, err = GenerateStatusUpdate(ctx)
	case "incident_report":
		content, err = GenerateIncidentReport(ctx)
	case "sensor_reading":
		content, err = GenerateSensorReading(ctx)
	case "social_media":
		content, err = GenerateSocialMedia(ctx)
	case "news_bulletin":
		content, err = GenerateNewsBulletin(ctx)
	case "logistics_report":
		content, err = GenerateLogisticsReport(ctx)
	case "personnel_status":
		content, err = GeneratePersonnelStatus(ctx)
	case "environmental_report":
		content, err = GenerateEnvironmentalReport(ctx)
	case "email":
		content, err = GenerateEmail(ctx)
	case "alert_message":
		content, err = GenerateAlertMessage(ctx)
	default:
		return nil, fmt.Errorf("unknown document type: %s", rule.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("generating %s: %w", rule.Type, err)
	}

	// Apply data pattern transformation
	content = ApplyDataPattern(content, rule.DataPattern, rule.SizeRange)

	// Ensure size is within range
	content = EnsureSizeRange(content, rule.SizeRange)

	return content, nil
}

// ApplyDataPattern applies a data pattern to the content.
func ApplyDataPattern(content []byte, pattern string, sizeRange [2]int64) []byte {
	switch pattern {
	case "random":
		// Replace part of the content with random bytes for worst-case dedup
		randomPortion := len(content) / 3
		if randomPortion > 0 {
			randomBytes := make([]byte, randomPortion)
			if _, err := rand.Read(randomBytes); err != nil {
				// Use deterministic fallback if crypto random fails
				for i := range randomBytes {
					randomBytes[i] = byte(i % 256)
				}
			}
			content = append(content, []byte("\n\n--- ENCRYPTED DATA ---\n")...)
			content = append(content, randomBytes...)
		}

	case "compressible":
		// Add repetitive structure for good compression
		repeated := bytes.Repeat([]byte("LOG_ENTRY: "), 10)
		content = append(content, []byte("\n\n--- SYSTEM LOGS ---\n")...)
		content = append(content, repeated...)

	case "realistic":
		// Keep content as-is - realistic mix
	}

	return content
}

// EnsureSizeRange pads or truncates content to fit within size range.
func EnsureSizeRange(content []byte, sizeRange [2]int64) []byte {
	minSize := int(sizeRange[0])
	maxSize := int(sizeRange[1])

	if len(content) < minSize {
		// Pad with whitespace and filler
		padding := minSize - len(content)
		filler := bytes.Repeat([]byte("\n"), padding/2)
		content = append(content, filler...)
		content = append(content, []byte(fmt.Sprintf("\n\n[Document padding: %d bytes]\n", padding))...)
		// Fill remaining with spaces
		remaining := minSize - len(content)
		if remaining > 0 {
			content = append(content, bytes.Repeat([]byte(" "), remaining)...)
		}
	} else if len(content) > maxSize {
		// Truncate with indication
		content = content[:maxSize-50]
		content = append(content, []byte("\n\n[Document truncated for size]\n")...)
	}

	return content
}

// ExecuteTemplate executes a template with the given data.
func ExecuteTemplate(tmpl string, data interface{}) (string, error) {
	t, err := template.New("doc").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// FormatTimestamp formats a timestamp for document headers.
func FormatTimestamp(t time.Time) string {
	return t.Format("2006-01-02 15:04:05 MST")
}

// FormatClassification returns a classification banner based on clearance level.
func FormatClassification(clearance int) string {
	switch clearance {
	case 5:
		return "╔════════════════════════════════════════════════════════════╗\n║                  TOP SECRET // SI // NOFORN                 ║\n╚════════════════════════════════════════════════════════════╝"
	case 4:
		return "╔════════════════════════════════════════════════════════════╗\n║                        SECRET // NOFORN                     ║\n╚════════════════════════════════════════════════════════════╝"
	case 3:
		return "═══════════════════════════════════════════════════════════\n                    CONFIDENTIAL\n═══════════════════════════════════════════════════════════"
	case 2:
		return "═══════════════════════════════════════════════════════════\n                FOR OFFICIAL USE ONLY\n═══════════════════════════════════════════════════════════"
	default:
		return "═══════════════════════════════════════════════════════════\n                    UNCLASSIFIED\n═══════════════════════════════════════════════════════════"
	}
}

// GenerateDocID generates a unique document ID.
func GenerateDocID(docType string, number int, timestamp time.Time) string {
	return fmt.Sprintf("%s-%s-%04d", strings.ToUpper(docType), timestamp.Format("20060102"), number)
}

// GetPhase returns which phase of the story we're in (1, 2, or 3).
func GetPhase(elapsed time.Duration) int {
	hours := elapsed.Hours()
	if hours < 24 {
		return 1 // First Contact
	} else if hours < 48 {
		return 2 // Invasion
	}
	return 3 // Resistance
}

// GetCasualtyCount returns estimated casualties based on story progression.
func GetCasualtyCount(elapsed time.Duration) int {
	hours := elapsed.Hours()
	if hours < 24 {
		return int(hours * 100) // Ramping up
	} else if hours < 48 {
		// Peak casualties during invasion
		return 2400 + int((hours-24)*5000)
	}
	// Resistance phase - casualties slow
	return 122400 + int((hours-48)*1000)
}

// GetThreatLevel returns the current threat level.
func GetThreatLevel(elapsed time.Duration) string {
	hours := elapsed.Hours()
	if hours < 6 {
		return "ELEVATED"
	} else if hours < 18 {
		return "HIGH"
	} else if hours < 24 {
		return "SEVERE"
	} else if hours < 48 {
		return "CRITICAL"
	} else if hours < 60 {
		return "CRITICAL"
	}
	return "SEVERE" // Resistance stabilizing
}

// RandomChoice returns a random element from a slice.
func RandomChoice(choices []string) string {
	if len(choices) == 0 {
		return ""
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Use deterministic fallback
		return choices[0]
	}
	idx := int(b[0]) % len(choices)
	return choices[idx]
}

// RandomInt returns a random integer between min and max (inclusive).
func RandomInt(min, max int) int {
	if min >= max {
		return min
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Use deterministic fallback
		return min
	}
	val := int(b[0])
	return min + (val % (max - min + 1))
}

// RandomFloat returns a random float between min and max.
func RandomFloat(min, max float64) float64 {
	if min >= max {
		return min
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Use deterministic fallback
		return min
	}
	val := float64(b[0]) / 255.0
	return min + val*(max-min)
}

// FormatCoordinates generates realistic military grid coordinates.
func FormatCoordinates(phase int) string {
	lat := RandomFloat(35.0, 45.0)
	lon := RandomFloat(-110.0, -95.0)
	return fmt.Sprintf("%.4f°N, %.4f°W (MGRS: 13TDE%04d%04d)",
		lat, math.Abs(lon), RandomInt(1000, 9999), RandomInt(1000, 9999))
}

// PhaseName returns the narrative name of the current phase.
func PhaseName(phase int) string {
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
