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
//   - Oversized paragraphs are force-split with overlap
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

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()

		// Markdown heading detection: always starts a new chunk
		if isMarkdownHeading(line) && buf.Len() > 0 {
			if err := p.emit(ctx, out, task, &idx, buf.String()); err != nil {
				return err
			}
			buf.Reset()
			buf.WriteString(line)
			buf.WriteByte('\n')
			continue
		}

		// Empty line = paragraph boundary
		if strings.TrimSpace(line) == "" && buf.Len() > 0 {
			// Only emit if buffer exceeds minimum chunk size (merge short paragraphs)
			if utf8.RuneCountInString(buf.String()) >= MinChunkSize {
				if err := p.emit(ctx, out, task, &idx, buf.String()); err != nil {
					return err
				}
				overlap := p.overlap(buf.String())
				buf.Reset()
				buf.WriteString(overlap)
			}
			// If too short, keep accumulating (paragraph merge)
			continue
		}

		buf.WriteString(line)
		buf.WriteByte('\n')

		// Force split for oversized paragraphs
		if buf.Len() >= p.cfg.MaxChunkSize {
			if err := p.emit(ctx, out, task, &idx, buf.String()); err != nil {
				return err
			}
			overlap := p.overlap(buf.String())
			buf.Reset()
			buf.WriteString(overlap)
		}
	}

	// Flush remaining content
	if buf.Len() > 0 {
		if err := p.emit(ctx, out, task, &idx, buf.String()); err != nil {
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

func (p *Parser) overlap(s string) string {
	if len(s) <= p.cfg.ChunkOverlap {
		return s
	}
	return s[len(s)-p.cfg.ChunkOverlap:]
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
