package llm

import (
	"fmt"
	"strings"
)

// PromptContext contains variables for template rendering
type PromptContext struct {
	// Document metadata
	DocType    string
	Format     string // "markdown" or "json"
	Author     string
	AuthorRole string
	Phase      int
	PhaseName  string

	// Story context
	TimelineEvent  string
	HoursIntoEvent int
	ThreatLevel    int
	CasualtyCount  int

	// Generation parameters
	TargetLength string // "500-1000 words", "1000-2000 entries", etc.
	Seed         int64  // For reproducibility

	// Template selection
	TemplateNum int
	VersionNum  int
}

// PromptBuilder constructs prompts for different document types
type PromptBuilder struct {
	templates map[string]string
}

// NewPromptBuilder creates a new prompt builder with default templates
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{
		templates: defaultTemplates(),
	}
}

// BuildPrompt generates a prompt for the given document type and context
func (pb *PromptBuilder) BuildPrompt(ctx PromptContext) GenerateRequest {
	template, exists := pb.templates[ctx.DocType]
	if !exists {
		// Fallback to generic template
		template = pb.templates["_generic"]
	}

	// Replace placeholders
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{doc_type}", ctx.DocType)
	prompt = strings.ReplaceAll(prompt, "{format}", ctx.Format)
	prompt = strings.ReplaceAll(prompt, "{author_name}", ctx.Author)
	prompt = strings.ReplaceAll(prompt, "{author_role}", ctx.AuthorRole)
	prompt = strings.ReplaceAll(prompt, "{phase}", fmt.Sprintf("%d", ctx.Phase))
	prompt = strings.ReplaceAll(prompt, "{phase_name}", ctx.PhaseName)
	prompt = strings.ReplaceAll(prompt, "{timeline_event}", ctx.TimelineEvent)
	prompt = strings.ReplaceAll(prompt, "{hours}", fmt.Sprintf("%d", ctx.HoursIntoEvent))
	prompt = strings.ReplaceAll(prompt, "{threat_level}", fmt.Sprintf("%d", ctx.ThreatLevel))
	prompt = strings.ReplaceAll(prompt, "{casualty_count}", fmt.Sprintf("%d", ctx.CasualtyCount))
	prompt = strings.ReplaceAll(prompt, "{target_length}", ctx.TargetLength)
	prompt = strings.ReplaceAll(prompt, "{clearance_level}", clearanceLevel(ctx.ThreatLevel))

	// Determine temperature based on version (more variety for later versions)
	temperature := 0.7 + (float64(ctx.VersionNum) * 0.05)
	if temperature > 0.9 {
		temperature = 0.9
	}

	return GenerateRequest{
		Prompt:      prompt,
		System:      systemPrompt(ctx),
		Temperature: temperature,
		MaxTokens:   0, // Let the model decide
	}
}

// systemPrompt creates a system message that enforces deduplication patterns
func systemPrompt(ctx PromptContext) string {
	return fmt.Sprintf(`You are generating realistic documents for an alien invasion scenario simulation.

CRITICAL DEDUPLICATION REQUIREMENTS:
1. Use SHARED BOILERPLATE (10%% of content):
   - Standard classification headers for %s clearance
   - Document ID formats: [DOC-{phase}-{author}-{type}-{num:04d}]
   - Standard footers with distribution lists

2. Use TEMPLATE SECTIONS (60%% of content):
   - Reusable paragraphs with variable placeholders
   - Standard report structures per document type
   - Common phrases and formulations

3. Create UNIQUE NARRATIVE (30%% of content):
   - Specific to this timeline event: %s
   - Author's voice and perspective
   - Contextual details

ENGAGEMENT REQUIREMENTS:
- Make this document INTERESTING to read
- Show human drama, heroism, horror, and hope
- Use vivid details and emotional weight
- This is not placeholder text - it's a story artifact

FORMAT: %s (strictly adhere to format requirements)
TEMPLATE VARIANT: %d of 10 (introduce controlled variation)
VERSION: %d of 4 (maintain consistency with previous versions)`,
		clearanceLevel(ctx.ThreatLevel),
		ctx.TimelineEvent,
		ctx.Format,
		ctx.TemplateNum,
		ctx.VersionNum)
}

// clearanceLevel maps threat level to classification
func clearanceLevel(threatLevel int) string {
	switch threatLevel {
	case 1:
		return "UNCLASSIFIED"
	case 2:
		return "CONFIDENTIAL"
	case 3:
		return "SECRET"
	case 4:
		return "TOP SECRET"
	case 5:
		return "TOP SECRET//SCI"
	default:
		return "CLASSIFIED"
	}
}

// defaultTemplates returns the prompt templates for each document type
func defaultTemplates() map[string]string {
	return map[string]string{
		"battle_report": `Generate a vivid military battle report from the {phase_name} phase of an alien invasion.

CONTEXT:
- Author: {author_name}, {author_role}, writing {hours} hours into the invasion
- Event: {timeline_event}
- Casualties: {casualty_count}
- Threat Level: {threat_level}/5

REQUIREMENTS:
- Open with tactical situation summary (unit positions, enemy strength, terrain)
- Include dramatic combat narrative with specific unit actions and heroism
- Detail weapons effectiveness (what works/doesn't work against alien tech)
- Casualty and equipment loss assessments with emotional weight
- Tactical recommendations for future engagements
- Classification: {clearance_level}
- Use military terminology and reporting format
- Make it feel REAL - this is a document someone would write under extreme stress

LENGTH: {target_length}`,

		"sitrep": `Generate a military situation report (SITREP) from the {phase_name} phase.

CONTEXT:
- Author: {author_name}, {author_role}
- Time: H+{hours} hours into invasion
- Event: {timeline_event}
- Threat: {threat_level}/5

REQUIREMENTS:
- Standard SITREP format:
  1. SITUATION: Current tactical situation
  2. ENEMY FORCES: Composition, disposition, capabilities
  3. FRIENDLY FORCES: Status, readiness, losses
  4. LOGISTICS: Supply status, critical shortages
  5. ASSESSMENT: Commander's analysis
  6. FORECAST: Next 12-24 hours
- Concise, factual, military tone
- Include unit designators, grid coordinates, time stamps
- Classification: {clearance_level}

LENGTH: {target_length}`,

		"intel_brief": `Generate a gripping intelligence brief about alien capabilities and intentions.

CONTEXT:
- Intelligence Officer: {author_name}, {author_role}
- Phase: {phase_name}
- Event: {timeline_event}
- Reliability: Based on threat level {threat_level}/5

REQUIREMENTS:
- KEY INTELLIGENCE SUMMARY (3-5 critical points)
- Source attribution and reliability assessment
- ANALYSIS: What we know vs. what we suspect
- Pattern recognition: Alien tactics, weak points, strategic objectives
- Threat assessment: Immediate dangers, long-term implications
- Actionable recommendations for command
- Make it feel like humanity is piecing together an alien puzzle with incomplete information

LENGTH: {target_length}`,

		"scientific_analysis": `Generate a fascinating scientific analysis document examining alien biology or technology.

CONTEXT:
- Researcher: Dr. {author_name}, {author_role}
- Phase: {phase_name}
- Discovery: {timeline_event}

REQUIREMENTS:
- Executive summary with key findings (3-5 bullet points)
- Detailed methodology section
- Observations: Physical characteristics, anomalies, unprecedented features
- Data tables: Measurements, chemical composition, energy readings (for JSON format)
- Theoretical implications: What this means for our understanding of biology/physics
- Recommendations: Further study, potential applications, containment protocols
- Include a sense of awe and scientific excitement/horror
- Technical but readable

For autopsy reports, include vivid anatomical details, organ systems that don't match Earth biology

LENGTH: {target_length}`,

		"lab_results": `Generate detailed laboratory analysis results for alien specimen or technology.

CONTEXT:
- Lab: {author_role}
- Researcher: Dr. {author_name}
- Subject ID: {timeline_event}
- Phase: {phase_name}

REQUIREMENTS:
- Sample identification and chain of custody
- Test protocols executed
- Quantitative results (chemical analysis, energy signatures, biological markers)
- Anomalies and unexpected findings
- Comparison to Earth-based analogues (where applicable)
- Safety warnings and handling protocols
- Data tables with measurements (for JSON: arrays of test results)
- Clinical, precise tone with moments of "this shouldn't be possible"

LENGTH: {target_length}`,

		"casualty_list": `Generate emotional but professional casualty documentation.

CONTEXT:
- Medical Officer: {author_name}
- Time: H+{hours} into invasion
- Estimated casualties: {casualty_count}
- Event: {timeline_event}

REQUIREMENTS (JSON format):
- Array of casualty records (1000-2000 entries)
- Fields per record:
  * Demographics: name, age, rank/civilian, unit, home_state, blood_type
  * Incident: injury_type, injury_cause, location, timestamp, alien_weapon_type
  * Medical: triage_category, treatment, prognosis, evacuation_priority
  * Status: KIA, WIA, MIA, critical, stable, discharged
  * Next of kin: name, relationship, notification_status
- Mix of military and civilian casualties
- Injury patterns showing conventional vs. alien weapons
- Some recoveries (hope amidst darkness)
- Realistic names and details

LENGTH: {target_length}`,

		"hospital_records": `Generate medical documentation from an overwhelmed facility during the invasion.

CONTEXT:
- Medical Officer: {author_name}
- Facility: Overwhelmed field hospital or military medical center
- Time: H+{hours} into invasion
- Patient load: Critical
- Event: {timeline_event}

REQUIREMENTS:
- Patient records with: name, rank/civilian, injuries (conventional vs. alien weapons), vitals, prognosis
- New injury patterns from alien weapons (energy burns, unknown pathogens, psychological trauma)
- Resource shortages: blood supply, burn treatment, morgue capacity
- Staff exhaustion notes
- Triage decisions (heartbreaking when resources are limited)
- Some recovery stories (hope amidst darkness)
- Professional tone masking emotional strain

LENGTH: {target_length}`,

		"intercepted_comms": `Generate eerie intercepted alien communications.

CONTEXT:
- Intelligence Officer: {author_name}
- Intercept time: H+{hours}
- Event: {timeline_event}
- Phase: {phase_name}

REQUIREMENTS:
- Include "translated" alien messages (eerie, inhuman perspective)
- Translation notes and confidence levels
- Linguistic analysis: syntax, unknown terms, cultural references
- Intelligence assessment: What does this reveal about alien intentions?
- Gaps in understanding (partial translation, context missing)
- Make it feel genuinely ALIEN - not human thoughts in different words
- Technical signals intelligence details (frequency, encryption, direction finding)

LENGTH: {target_length}`,

		"memo": `Generate {doc_type} showing the bureaucratic side of an alien invasion.

CONTEXT:
- Author: {author_name}, {author_role}
- Phase: {phase_name}
- Context: {timeline_event}

REQUIREMENTS:
- Bureaucratic but urgent tone
- Trying to maintain normalcy while world ends
- Administrative concerns during crisis:
  * Personnel reassignments
  * Supply chain disruptions
  * Policy changes for emergency conditions
  * Coordination between agencies/units
- Formal memo format with TO/FROM/RE/DATE
- Subtext: Fear, uncertainty, attempting to maintain order
- Show the strain of civilization trying to function during apocalypse

LENGTH: {target_length}`,

		"press_release": `Generate official press release managing public information during the invasion.

CONTEXT:
- Author: {author_name}, {author_role}
- Phase: {phase_name}
- Event: {timeline_event}
- Hours into crisis: {hours}

REQUIREMENTS:
- Official tone, careful wording
- Balancing truth with morale
- Managing public panic
- Information about:
  * Current situation (sanitized for public)
  * Government response and capabilities
  * Citizen safety instructions
  * Reassurance (genuine or otherwise)
- Attribution: official spokesperson, agency
- FOR IMMEDIATE RELEASE header
- Contact information
- Show tension between transparency and security

LENGTH: {target_length}`,

		"evacuation_order": `Generate evacuation order for civilian population.

CONTEXT:
- Authority: {author_name}, {author_role}
- Location: Endangered area
- Event: {timeline_event}
- Time remaining: Limited

REQUIREMENTS:
- Clear, direct instructions
- Assembly points with grid coordinates or addresses
- What to bring (essentials only): documents, medications, water, 48-hour supplies
- Routes to safety
- Time limits and urgency level
- Consequences of non-compliance
- Special provisions: elderly, disabled, children
- Emergency contact numbers
- Official authority stamps/signatures
- Show the strain: this is a desperate measure

LENGTH: {target_length}`,

		"private_diary": `Generate personal diary entry showing individual perspective on the invasion.

CONTEXT:
- Person: {author_name} ({author_role})
- Phase: {phase_name}
- Time: {hours} hours into event
- Situation: {timeline_event}

REQUIREMENTS:
- First-person, emotional, immediate
- Stream of consciousness style
- Personal observations: What they've seen, who they've lost, moments of humanity
- Memories of "before" (normal life now lost)
- Fear, hope, determination, despair, dark humor
- Authentic voice - this is a PERSON, not a report
- Make readers FEEL the invasion through individual eyes
- Raw, unfiltered thoughts
- May be fragmented, rushed (writing during crisis)

LENGTH: {target_length}`,

		"social_media": `Generate social media post during the alien invasion.

CONTEXT:
- User: {author_name}
- Platform: Twitter/Facebook-style
- Time: H+{hours}
- Event: {timeline_event}

REQUIREMENTS:
- Short, urgent, immediate
- Social media style: hashtags, @mentions, informal
- Could be:
  * Eyewitness account of invasion
  * Spreading panic/conspiracy theories (if author is "Eve" - adversarial)
  * Checking on loved ones
  * Sharing survival tips
  * Undermining official narratives (adversarial content)
  * Calls for help
- Authentic social media voice
- Emotional, raw, real-time
- For adversarial content: sow confusion, spread misinformation

LENGTH: {target_length}`,

		"email": `Generate email correspondence during the invasion.

CONTEXT:
- From: {author_name} ({author_role})
- Phase: {phase_name}
- Context: {timeline_event}
- Time: H+{hours}

REQUIREMENTS:
- Standard email format (To/From/Subject/Body)
- Could be:
  * Checking on loved ones
  * Last messages ("if you get this...")
  * Coordinating survival/evacuation
  * Professional correspondence during crisis
  * Goodbye messages
- Mix of professional and personal depending on context
- Show communication infrastructure breaking down (if late phase)
- Authentic voice for relationship (family/colleague/friend)

LENGTH: {target_length}`,

		// Generic fallback template
		"_generic": `Generate a {doc_type} document from an alien invasion scenario.

CONTEXT:
- Author: {author_name}, {author_role}
- Phase: {phase_name} (phase {phase})
- Event: {timeline_event}
- Time: H+{hours} hours
- Threat Level: {threat_level}/5

REQUIREMENTS:
- Create realistic, engaging content appropriate for document type: {doc_type}
- Format: {format}
- Classification: {clearance_level}
- Make it feel authentic and emotionally resonant
- Show human perspective on extraordinary events
- Include appropriate technical/professional details for the role

LENGTH: {target_length}`,
	}
}
