## generate-s3bench-data

Content generation tool for s3bench scenarios using Ollama LLM.

### Purpose

Generates realistic, story-driven document content for the `alien_invasion` scenario to:
1. **Test deduplication** - Creates 3-5x dedup ratio via shared templates and boilerplate
2. **Stress test viewers** - Produces complex JSON structures and long markdown documents
3. **Improve demos** - Makes the app more engaging with compelling narrative content

### Requirements

- **Ollama running** with `gpt-oss:120b` model loaded
- **Network access** to Ollama endpoint (default: `honker:11434`)
- **Disk space** for generated content (~200+ MB)

### Usage

#### Quick Start

```bash
# Using Makefile (recommended)
make generate-s3bench-data

# Direct invocation
go run cmd/generate-s3bench-data/main.go \
    --scenario alien_invasion \
    --ollama-endpoint http://honker:11434 \
    --ollama-model gpt-oss:120b \
    --output internal/s3bench/data/alien_invasion/ \
    --seed 42 \
    --concurrency 10
```

#### Flags

- `--scenario` - Scenario name (default: `alien_invasion`)
- `--ollama-endpoint` - Ollama API URL (default: `http://honker:11434`)
- `--ollama-model` - Model to use (default: `gpt-oss:120b`)
- `--output` - Output directory (default: `internal/s3bench/data/alien_invasion/`)
- `--seed` - Random seed for reproducibility (default: `42`)
- `--concurrency` - Parallel LLM requests (default: `10`)
- `--dry-run` - Show task list without generating
- `--verbose` - Enable debug logging

#### Dry Run

See what will be generated without calling Ollama:

```bash
go run cmd/generate-s3bench-data/main.go --dry-run
```

### Output Structure

```
internal/s3bench/data/alien_invasion/
├── index.json                    # Content index with metadata
├── README.md                     # Generation details
├── battle_report/
│   ├── p1/                       # Phase 1
│   │   └── commander/
│   │       ├── t1_v1.md
│   │       ├── t1_v2.md
│   │       └── ...
│   ├── p2/                       # Phase 2
│   └── p3/                       # Phase 3
├── casualty_list/
│   └── p1/
│       └── commander/
│           ├── t1_v1.json
│           └── ...
└── ... (31 document types total)
```

### Generation Process

1. **Load scenario** - Read alien_invasion story definition
2. **Build task matrix** - 31 doc types × 3 phases × authors × 10 templates × versions
3. **Generate content** - Parallel LLM calls with progress bar
4. **Create index** - Metadata for content loader
5. **Validate** - Check JSON syntax, file sizes, structure
6. **Summary** - Report success, errors, statistics

### Expected Output

```
Connecting to Ollama at http://honker:11434...
✓ Model gpt-oss:120b available
Generating content for alien_invasion scenario (31 document types)...
[████████████████████████████████] 2847/2847 (100%) - 15m 23s

============================================================
  Content Generation Complete
============================================================

✓ Generated 2,847 documents in 15m 23s
✓ Markdown: 1,708 files
✓ JSON: 1,139 files
✓ Total size: 212 MB
✓ Estimated dedup ratio: 3.8x

Index written to: index.json
README written to: README.md
```

### Deduplication Strategy

Content is designed for **3-5x deduplication** via:

- **10% shared boilerplate** - Identical across documents:
  - Classification headers (5 templates by clearance level)
  - Standard footers (10 variants)
  - Document ID formats

- **60% template sections** - Reusable paragraphs with variables:
  - 10 template variants per document type
  - Variables: `{time}`, `{location}`, `{unit_id}`, `{casualties}`
  - Example: "At {time}, unit {unit_id} engaged at {location}"

- **30% unique narrative** - Context-specific content:
  - Timeline events, phase progression, author voice
  - Situational details unique to that document

### Error Handling

- **Retries** - 3 attempts per failed LLM call
- **Partial generation** - Continues on failure (some files OK)
- **Error log** - Failures written to `generation_errors.log`
- **Non-fatal** - Benchmark falls back to lorem ipsum when content unavailable

### Regeneration

To regenerate after updates:

```bash
# Clean old content
make clean-s3bench-data

# Generate fresh
make generate-s3bench-data
```

### CI Behavior

Content generation is **NOT** run in CI. The benchmark:
- Detects missing content directory
- Falls back to lorem ipsum generation
- Zero external dependencies for tests

### Troubleshooting

**"Ollama not available"**
- Check Ollama is running: `curl http://honker:11434/api/tags`
- Verify model loaded: `ollama list | grep gpt-oss`

**"Model not found"**
- Pull model: `ollama pull gpt-oss:120b`

**Generation too slow**
- Increase `--concurrency` (default 10)
- Use smaller model for testing
- Run dry-run first to verify task count

**Out of disk space**
- Content is ~200+ MB
- Clean with: `make clean-s3bench-data`
