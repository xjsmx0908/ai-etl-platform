// Package parser provides HTTP client for Document Parser Service
package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/model"
)

// Client wraps HTTP calls to Parser Service
type Client struct {
	endpoint   string
	httpClient *http.Client
}

type ParseResult struct {
	Chunks    []model.Chunk
	PageCount int
	PageStart int
	PageEnd   int
}

// NewClient creates a new parser service client
func NewClient(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
		httpClient: &http.Client{
			// Scanned PDFs go through OCR, which can take minutes for a large
			// document; a short timeout would make every such task retry-fail.
			Timeout: 600 * time.Second,
		},
	}
}

// ParseFile sends file to parser service and returns chunks
func (c *Client) ParseFile(ctx context.Context, task model.Task) ([]model.Chunk, error) {
	result, err := c.parseFileRange(ctx, task, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	return result.Chunks, nil
}

// ParseFileRange parses a bounded PDF page range. page_end is exclusive; zero
// means the parser should process the whole document. chunkIndexOffset keeps
// chunk IDs stable when a document is processed in multiple OCR batches.
func (c *Client) ParseFileRange(ctx context.Context, task model.Task, pageStart, pageEnd, chunkIndexOffset int) ([]model.Chunk, error) {
	result, err := c.parseFileRange(ctx, task, pageStart, pageEnd, chunkIndexOffset)
	if err != nil {
		return nil, err
	}
	return result.Chunks, nil
}

// ParseFileRangeResult is the page-aware variant used by the OCR pipeline.
// PageCount is the complete source document page count, even when only a range
// was parsed.
func (c *Client) ParseFileRangeResult(ctx context.Context, task model.Task, pageStart, pageEnd, chunkIndexOffset int) (ParseResult, error) {
	return c.parseFileRange(ctx, task, pageStart, pageEnd, chunkIndexOffset)
}

func (c *Client) parseFileRange(ctx context.Context, task model.Task, pageStart, pageEnd, chunkIndexOffset int) (ParseResult, error) {
	// Open file
	file, err := os.Open(task.FilePath)
	if err != nil {
		return ParseResult{}, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// Prepare multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add form fields
	_ = writer.WriteField("doc_id", task.DocID)
	_ = writer.WriteField("tenant_id", task.TenantID)
	if task.Permission != "" {
		_ = writer.WriteField("permission", task.Permission)
	}
	if task.FileHash != "" {
		_ = writer.WriteField("file_hash", task.FileHash)
	}
	if pageStart > 0 {
		_ = writer.WriteField("page_start", fmt.Sprintf("%d", pageStart))
	}
	if pageEnd > 0 {
		_ = writer.WriteField("page_end", fmt.Sprintf("%d", pageEnd))
	}
	if chunkIndexOffset > 0 {
		_ = writer.WriteField("chunk_index_offset", fmt.Sprintf("%d", chunkIndexOffset))
	}
	if len(task.Metadata) > 0 {
		if data, err := json.Marshal(task.Metadata); err == nil {
			_ = writer.WriteField("metadata", string(data))
		}
	}

	// Add file
	part, err := writer.CreateFormFile("file", task.FilePath)
	if err != nil {
		return ParseResult{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return ParseResult{}, fmt.Errorf("copy file: %w", err)
	}

	if err := writer.Close(); err != nil {
		return ParseResult{}, fmt.Errorf("close writer: %w", err)
	}

	// Create request
	url := fmt.Sprintf("%s/api/v1/parse", c.endpoint)
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return ParseResult{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if internalToken := config.EnvSecret("PARSER_INTERNAL_TOKEN", ""); internalToken != "" {
		req.Header.Set("X-Internal-Token", internalToken)
	}

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ParseResult{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return ParseResult{}, fmt.Errorf("parser service error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Parse response
	var parseResp struct {
		DocID    string `json:"doc_id"`
		TenantID string `json:"tenant_id"`
		Chunks   []struct {
			ChunkID    string            `json:"chunk_id"`
			DocID      string            `json:"doc_id"`
			TenantID   string            `json:"tenant_id"`
			Content    string            `json:"content"`
			Index      int               `json:"index"`
			TokenCount *int              `json:"token_count"`
			Permission *string           `json:"permission"`
			FileHash   *string           `json:"file_hash"`
			Metadata   map[string]string `json:"metadata"`
		} `json:"chunks"`
		TotalChunks   int     `json:"total_chunks"`
		ParseTimeMs   float64 `json:"parse_time_ms"`
		FileSizeBytes int     `json:"file_size_bytes"`
		Status        string  `json:"status"`
		PageCount     int     `json:"page_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&parseResp); err != nil {
		return ParseResult{}, fmt.Errorf("decode response: %w", err)
	}

	// Convert to model.Chunk
	chunks := make([]model.Chunk, 0, len(parseResp.Chunks))
	for _, c := range parseResp.Chunks {
		chunk := model.Chunk{
			ChunkID:  c.ChunkID,
			DocID:    c.DocID,
			TenantID: c.TenantID,
			Content:  c.Content,
			Index:    c.Index,
		}
		if c.TokenCount != nil {
			chunk.TokenUsed = *c.TokenCount
		}
		if c.Permission != nil {
			chunk.Permission = *c.Permission
		}
		if c.FileHash != nil {
			chunk.FileHash = *c.FileHash
		}
		if len(c.Metadata) > 0 {
			chunk.Metadata = c.Metadata
		}
		chunks = append(chunks, chunk)
	}

	return ParseResult{Chunks: chunks, PageCount: parseResp.PageCount, PageStart: pageStart, PageEnd: pageEnd}, nil
}
