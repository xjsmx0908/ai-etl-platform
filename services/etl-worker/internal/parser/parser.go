// Package parser implements streaming document parsing with semantic chunking.
// Supports Markdown heading-aware splitting, paragraph merging, and O(1) memory usage.
package parser

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/s3"
)

// MinChunkSize defines the minimum character count before a chunk is emitted.
// Short paragraphs below this threshold are merged with the next one.
const MinChunkSize = 128

// Parser provides streaming file parsing for various document formats.
// Supported: .txt, .md (direct read), .pdf (pdftotext), .docx (pandoc)
type Parser struct {
	cfg            config.Config
	metrics        *metrics.Collector
	objectStore    *s3.Client
	objectStoreErr error
	objectStoreMu  sync.Once
}

// New creates a parser with the given configuration.
func New(cfg config.Config, m *metrics.Collector) *Parser {
	return &Parser{cfg: cfg, metrics: m}
}

// ParseStream reads a file and emits semantic chunks to the output channel.
// Chunking strategy:
//   - Markdown headings (# ## ### etc.) always start a new chunk
//   - Empty lines act as paragraph boundaries
//   - Short paragraphs (< MinChunkSize) are merged with the next one
//   - Oversized paragraphs are force-split with overlap, and every cut is
//     pulled back to a sentence or line boundary so no chunk starts or ends
//     mid-sentence
//
// The channel is closed when parsing completes or an error occurs.
func (p *Parser) ParseStream(ctx context.Context, task model.Task, out chan<- model.Chunk) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("parser panic doc=%s: %v", task.DocID, r)
		}
	}()
	defer close(out)

	start := time.Now()

	reader, cleanup, size, err := p.openFile(ctx, task.FilePath)
	if err != nil {
		return fmt.Errorf("open file %s: %w", task.FilePath, err)
	}
	defer cleanup()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, p.cfg.ReadBufferSize), p.cfg.MaxChunkSize*2)

	var buf strings.Builder
	idx := 0

	// overlapOnly tracks whether the buffer holds nothing but the overlap
	// carried over from the previous force split. Such a buffer is a pure
	// duplicate of the previous chunk's tail and must not be emitted on its own.
	overlapOnly := false

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()

		// Markdown heading detection: always starts a new chunk
		if isMarkdownHeading(line) && buf.Len() > 0 {
			if err := p.emitOversized(ctx, out, task, &idx, buf.String()); err != nil {
				return err
			}
			buf.Reset()
			buf.WriteString(line)
			buf.WriteByte('\n')
			overlapOnly = false
			continue
		}

		// Empty line = paragraph boundary
		if strings.TrimSpace(line) == "" && buf.Len() > 0 {
			// Only emit if buffer exceeds minimum chunk size (merge short paragraphs)
			if utf8.RuneCountInString(buf.String()) >= MinChunkSize {
				if err := p.emitOversized(ctx, out, task, &idx, buf.String()); err != nil {
					return err
				}
				// Paragraph boundaries are semantic boundaries. Overlap only
				// helps when force-splitting one oversized body; copying a whole
				// paragraph into the next chunk creates near-duplicate chunks and
				// lets repeated headers dominate retrieval. The parser service
				// chunker behaves the same way.
				buf.Reset()
				overlapOnly = false
			}
			// If too short, keep accumulating (paragraph merge)
			continue
		}

		buf.WriteString(line)
		buf.WriteByte('\n')
		overlapOnly = false

		// Force split for oversized paragraphs. MaxChunkSize is a character
		// budget (the same unit the parser service and .env.example document),
		// so it is measured in runes, and the cut is pulled back to a natural
		// boundary: a byte-length test both let a single long line through
		// whole and cut sentences in half.
		if utf8.RuneCountInString(buf.String()) >= p.cfg.MaxChunkSize {
			if err := p.emitOversized(ctx, out, task, &idx, buf.String()); err != nil {
				return err
			}
			overlap := p.overlap(buf.String())
			buf.Reset()
			buf.WriteString(overlap)
			overlapOnly = overlap != ""
		}
	}

	// Flush remaining content. A buffer holding nothing but the carried overlap
	// repeats the previous chunk, so it is skipped.
	if buf.Len() > 0 && !overlapOnly {
		if err := p.emitOversized(ctx, out, task, &idx, buf.String()); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	p.metrics.Emit(model.Metric{
		DocID: task.DocID, TenantID: task.TenantID,
		Stage: "parse", Duration: time.Since(start), Success: true,
	})

	slog.Info("parse complete", "doc_id", task.DocID, "chunks", idx,
		"size", FmtBytes(size), "duration", time.Since(start))
	return nil
}

// isMarkdownHeading detects lines starting with 1-6 '#' followed by a space.
func isMarkdownHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 2 {
		return false
	}
	level := 0
	for _, ch := range trimmed {
		if ch == '#' {
			level++
		} else {
			break
		}
	}
	return level >= 1 && level <= 6 && len(trimmed) > level && trimmed[level] == ' '
}

func (p *Parser) openFile(ctx context.Context, path string) (io.Reader, func(), int64, error) {
	info, err := os.Stat(path)
	if err == nil {
		return p.openPath(ctx, path, info.Size())
	}

	if p.cfg.IsDev() {
		return nil, nil, 0, fmt.Errorf("file not found: %w", err)
	}

	// In non-dev environments, task file paths can be object keys in S3/MinIO.
	// If local file is missing, fetch object to a temp file and parse from disk.
	localPath, objectSize, objectCleanup, fetchErr := p.materializeObject(ctx, path)
	if fetchErr != nil {
		return nil, nil, 0, fmt.Errorf("file not found locally and object fetch failed: %w", fetchErr)
	}

	reader, cleanup, size, openErr := p.openPath(ctx, localPath, objectSize)
	if openErr != nil {
		objectCleanup()
		return nil, nil, 0, openErr
	}
	combinedCleanup := func() {
		cleanup()
		objectCleanup()
	}

	return reader, combinedCleanup, size, nil
}

func (p *Parser) openPath(ctx context.Context, path string, size int64) (io.Reader, func(), int64, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".txt", ".md", ".csv", ".log":
		return p.openDirect(path, size)
	case ".pdf":
		return p.openPDF(ctx, path, size)
	case ".docx", ".doc", ".rtf", ".odt":
		return p.openDocx(ctx, path, size)
	default:
		return p.openDirect(path, size)
	}
}

// MaterializeObject downloads an object key to a local temp file and returns
// its path plus a cleanup func. Used when the parser service needs a local path
// (PDF/DOCX/OCR require the python service, not the local text scanner).
func (p *Parser) MaterializeObject(ctx context.Context, key string) (string, int64, func(), error) {
	return p.materializeObject(ctx, key)
}

func (p *Parser) materializeObject(ctx context.Context, key string) (string, int64, func(), error) {
	client, err := p.getObjectStoreClient()
	if err != nil {
		return "", 0, nil, err
	}

	obj, err := client.Download(ctx, key)
	if err != nil {
		return "", 0, nil, fmt.Errorf("download object %s: %w", key, err)
	}
	defer obj.Close()

	ext := strings.ToLower(filepath.Ext(key))
	tmp, err := os.CreateTemp("", "ai-etl-object-*"+ext)
	if err != nil {
		return "", 0, nil, fmt.Errorf("create temp file: %w", err)
	}
	defer tmp.Close()

	size, err := io.Copy(tmp, obj)
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", 0, nil, fmt.Errorf("copy object to temp file: %w", err)
	}

	slog.Info("materialized object to local temp file",
		"object_key", key, "temp_path", tmp.Name(), "size", FmtBytes(size))

	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}
	return tmp.Name(), size, cleanup, nil
}

func (p *Parser) getObjectStoreClient() (*s3.Client, error) {
	p.objectStoreMu.Do(func() {
		p.objectStore, p.objectStoreErr = s3.New(s3.Config{
			Endpoint:   p.cfg.S3Endpoint,
			AccessKey:  p.cfg.S3AccessKey,
			SecretKey:  p.cfg.S3SecretKey,
			Bucket:     p.cfg.S3Bucket,
			UseSSL:     p.cfg.S3UseSSL,
			AutoCreate: false,
		})
	})
	if p.objectStoreErr != nil {
		return nil, p.objectStoreErr
	}
	return p.objectStore, nil
}

func (p *Parser) openDirect(path string, size int64) (io.Reader, func(), int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	return bufio.NewReaderSize(f, p.cfg.ReadBufferSize), func() { f.Close() }, size, nil
}

func (p *Parser) openPDF(ctx context.Context, path string, size int64) (io.Reader, func(), int64, error) {
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", "-enc", "UTF-8", path, "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("pdftotext pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, 0, fmt.Errorf("pdftotext start: %w (is poppler-utils installed?)", err)
	}
	cleanup := func() { _ = cmd.Wait() }
	slog.Debug("parsing PDF via pdftotext", "path", path, "size", FmtBytes(size))
	return stdout, cleanup, size, nil
}

func (p *Parser) openDocx(ctx context.Context, path string, size int64) (io.Reader, func(), int64, error) {
	cmd := exec.CommandContext(ctx, "pandoc", "-f", "docx", "-t", "plain", "--wrap=none", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("pandoc pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, 0, fmt.Errorf("pandoc start: %w (is pandoc installed?)", err)
	}
	cleanup := func() { _ = cmd.Wait() }
	slog.Debug("parsing DOCX via pandoc", "path", path, "size", FmtBytes(size))
	return stdout, cleanup, size, nil
}

func (p *Parser) emit(ctx context.Context, out chan<- model.Chunk, task model.Task, idx *int, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	chunk := model.Chunk{
		ChunkID:    fmt.Sprintf("%s_%04d", task.DocID, *idx),
		DocID:      task.DocID,
		TenantID:   task.TenantID,
		Content:    content,
		Index:      *idx,
		CreatedAt:  time.Now(),
		Permission: task.Permission,
		FileHash:   task.FileHash,
		Metadata:   copyStringMap(task.Metadata),
	}
	*idx++
	select {
	case out <- chunk:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Boundary characters used when force-splitting a body that carries no
// structural markers. Strongest first: a line break, then a sentence
// terminator, then a clause separator. Ending a piece just after one of these
// keeps whole sentences inside a single chunk instead of cutting a sentence in
// half across two chunks -- a split sentence is retrievable by neither.
const (
	lineBreak           = "\n"
	sentenceTerminators = "。！？!?；;…"
	clauseSeparators    = "，、,：:"
	boundaryChars       = lineBreak + sentenceTerminators + clauseSeparators
)

// splitBoundaryEnd returns the largest rune offset <= limit that lands just
// after a natural boundary, falling back to limit when the window holds none
// (for example one unbroken sentence longer than maxSize).
func splitBoundaryEnd(runes []rune, start, limit, minSize int) int {
	if limit >= len(runes) {
		return len(runes)
	}
	floor := start + minSize
	if floor <= start {
		floor = start + 1
	}
	if floor >= limit {
		return limit
	}
	for _, chars := range []string{lineBreak, sentenceTerminators, clauseSeparators} {
		for offset := limit; offset > floor; offset-- {
			if strings.ContainsRune(chars, runes[offset-1]) {
				return offset
			}
		}
	}
	return limit
}

// snapToBoundary moves candidate forward to the next boundary start so a chunk
// never begins mid-sentence. It returns candidate unchanged when no boundary
// exists before limit.
func snapToBoundary(runes []rune, candidate, limit int) int {
	if candidate <= 0 {
		return 0
	}
	if strings.ContainsRune(boundaryChars, runes[candidate-1]) {
		return candidate
	}
	for offset := candidate + 1; offset < limit; offset++ {
		if strings.ContainsRune(boundaryChars, runes[offset-1]) {
			return offset
		}
	}
	return candidate
}

// splitOversized splits text into pieces of at most maxSize runes, each ending
// just after a natural boundary and carrying a rune-safe overlap into the next
// piece. It mirrors the parser service's force split so both chunkers agree on
// where a chunk ends.
func splitOversized(text string, maxSize, overlap, minSize int) []string {
	runes := []rune(text)
	if len(runes) <= maxSize {
		return []string{text}
	}
	pieces := make([]string, 0, len(runes)/maxSize+1)
	pos := 0
	for pos < len(runes) {
		limit := pos + maxSize
		if limit > len(runes) {
			limit = len(runes)
		}
		end := splitBoundaryEnd(runes, pos, limit, minSize)
		if piece := strings.TrimSpace(string(runes[pos:end])); piece != "" {
			pieces = append(pieces, piece)
		}
		if end >= len(runes) {
			break
		}
		// Move the start inside the overlap window, but never backwards (a
		// stalled position would loop forever) and never mid-sentence.
		next := end - overlap
		if next < 0 {
			next = 0
		}
		next = snapToBoundary(runes, next, end)
		if next > pos {
			pos = next
		} else {
			pos = end
		}
	}
	return pieces
}

// emitOversized emits content as one or more boundary-aligned chunks.
func (p *Parser) emitOversized(ctx context.Context, out chan<- model.Chunk, task model.Task, idx *int, content string) error {
	for _, piece := range splitOversized(content, p.cfg.MaxChunkSize, p.cfg.ChunkOverlap, MinChunkSize) {
		if err := p.emit(ctx, out, task, idx, piece); err != nil {
			return err
		}
	}
	return nil
}

// overlap returns the trailing overlap window of s, snapped forward to a
// boundary and never cut mid-rune. Slicing bytes here used to both mangle
// multi-byte characters and hand the next chunk a half-sentence to open with.
func (p *Parser) overlap(s string) string {
	if p.cfg.ChunkOverlap <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= p.cfg.ChunkOverlap {
		return s
	}
	tail := runes[len(runes)-p.cfg.ChunkOverlap:]
	for offset := 1; offset < len(tail); offset++ {
		if strings.ContainsRune(boundaryChars, tail[offset-1]) {
			return string(tail[offset:])
		}
	}
	return string(tail)
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// FmtBytes formats byte counts in human-readable form.
func FmtBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
