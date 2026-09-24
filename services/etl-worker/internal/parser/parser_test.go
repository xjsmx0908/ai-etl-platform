package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
)

// Regression tests for the local plain-text scanner. .txt/.md/.csv/.log uploads
// are chunked by this scanner (binary formats go to the parser service), and its
// semantics are meant to match the parser service's chunker. The scanner used to
// slice its overlap window by byte, which cut multi-byte characters in half and
// then emitted the mangled fragment as its own trailing chunk; a line longer
// than MaxChunkSize was never split at all. See docs/optimization-plan.md §1.3
// 缺陷 21.

const probeLongSentence = "当同一份文档在入库过程中被中断并重新投递时系统必须依据检查点恢复已完成的解析与向量化结果" +
	"而不能把整份文档从头重新处理一遍否则既浪费算力也会因为两次运行之间的切块边界差异而在检索" +
	"索引里留下两份内容相近但身份不同的块进而让同一个问句在两次检索之间返回不同的证据集合"

const probeTailSentence = "后续小节继续描述其余通道的观测口径与保留期限。"

func testConfig(maxChunkSize, chunkOverlap int) config.Config {
	return config.Config{
		MaxChunkSize:   maxChunkSize,
		ChunkOverlap:   chunkOverlap,
		ReadBufferSize: 1 << 16,
	}
}

// numberedSentences returns distinct sentences so a chunk's content can be
// located in the source text without matching an earlier repetition.
func numberedSentences(count int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		b.WriteString(fmt.Sprintf("第%04d句说明了一个独立的业务规则并给出处理口径。", i))
	}
	return b.String()
}

// unstructuredProbeText is one paragraph with no blank lines and no headings,
// longer than MaxChunkSize, whose only terminator-free sentence straddles the
// cut. This is the shape the deployed defect was reproduced with.
func unstructuredProbeText() string {
	filler := []rune(numberedSentences(24))[:500]
	return string(filler) + probeLongSentence + strings.Repeat(probeTailSentence, 2)
}

func parseText(t *testing.T, text string, cfg config.Config) []model.Chunk {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}

	collector := metrics.NewCollector(16)
	defer collector.Stop()

	out := make(chan model.Chunk, 64)
	errs := make(chan error, 1)
	p := New(cfg, collector)
	go func() {
		errs <- p.ParseStream(context.Background(), model.Task{
			DocID: "doc-1", TenantID: "tenant-a", FilePath: path,
		}, out)
	}()

	var chunks []model.Chunk
	for chunk := range out {
		chunks = append(chunks, chunk)
	}
	if err := <-errs; err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	return chunks
}

func head(content string, n int) string {
	runes := []rune(content)
	if len(runes) <= n {
		return content
	}
	return string(runes[:n])
}

func tail(content string, n int) string {
	runes := []rune(content)
	if len(runes) <= n {
		return content
	}
	return string(runes[len(runes)-n:])
}

// A chunk carrying a broken character is corrupt evidence: it can never match a
// query, and it shows the cut landed inside a multi-byte character.
func TestParseStreamOverlapKeepsUTF8Intact(t *testing.T) {
	chunks := parseText(t, unstructuredProbeText(), testConfig(600, 50))
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized paragraph to split, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk.Content) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, chunk.Content)
		}
		if strings.ContainsRune(chunk.Content, utf8.RuneError) {
			t.Errorf("chunk %d contains a broken character: %q", i, chunk.Content)
		}
	}
}

// The overlap carried across a force split must not be emitted as a chunk of its
// own: it repeats the previous chunk's tail and adds nothing.
func TestParseStreamDoesNotEmitOverlapOnlyTail(t *testing.T) {
	chunks := parseText(t, unstructuredProbeText(), testConfig(600, 50))
	contents := make([]string, len(chunks))
	for i, chunk := range chunks {
		contents[i] = chunk.Content
	}
	for i, outer := range contents {
		for j, inner := range contents {
			if i == j {
				continue
			}
			if utf8.RuneCountInString(inner) < utf8.RuneCountInString(outer) && strings.Contains(outer, inner) {
				t.Errorf("chunk %d repeats chunk %d: %q", j, i, head(inner, 40))
			}
		}
	}
}

// MaxChunkSize is documented as a character budget, so a chunk must never hold
// more runes than that -- whatever the byte length of the script.
func TestParseStreamBoundsChunkSize(t *testing.T) {
	cfg := testConfig(600, 50)
	chunks := parseText(t, unstructuredProbeText(), cfg)
	for i, chunk := range chunks {
		if n := utf8.RuneCountInString(chunk.Content); n > cfg.MaxChunkSize {
			t.Errorf("chunk %d holds %d runes, over MaxChunkSize=%d", i, n, cfg.MaxChunkSize)
		}
	}
}

// A terminator-free sentence longer than the distance between two cuts must
// still be whole in exactly one chunk, and no chunk may start or end inside a
// sentence.
func TestParseStreamSplitsOnSentenceBoundaries(t *testing.T) {
	text := unstructuredProbeText()
	chunks := parseText(t, text, testConfig(600, 50))

	holders := 0
	for _, chunk := range chunks {
		if strings.Contains(chunk.Content, probeLongSentence) {
			holders++
		}
	}
	if holders == 0 {
		t.Errorf("the terminator-free sentence was cut apart: no chunk holds it whole")
	}

	terminators := "。！？；\n"
	for i, chunk := range chunks {
		start := strings.Index(text, chunk.Content)
		if start > 0 {
			before := []rune(text[:start])
			if !strings.ContainsRune(terminators, before[len(before)-1]) {
				t.Errorf("chunk %d starts mid-sentence: %q", i, head(chunk.Content, 40))
			}
		}
		if !strings.HasSuffix(chunk.Content, "。") {
			t.Errorf("chunk %d ends mid-sentence: %q", i, tail(chunk.Content, 40))
		}
	}
}

// Semantics that must survive the boundary fix.
func TestParseStreamHeadingStartsNewChunk(t *testing.T) {
	text := "# 标题一\n" + strings.Repeat("第一章的正文说明了一个独立的业务规则并给出处理口径。", 8) +
		"\n# 标题二\n" + strings.Repeat("第二章的正文说明了一个独立的业务规则并给出处理口径。", 8)
	chunks := parseText(t, text, testConfig(4000, 0))
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].Content, "# 标题一") {
		t.Errorf("chunk 0 does not start at the first heading: %q", head(chunks[0].Content, 20))
	}
	if !strings.HasPrefix(chunks[1].Content, "# 标题二") {
		t.Errorf("chunk 1 does not start at the second heading: %q", head(chunks[1].Content, 20))
	}
}

func TestParseStreamMergesShortParagraphs(t *testing.T) {
	text := "短句一。\n\n短句二。\n\n" + strings.Repeat("第三段说明了一个独立的业务规则并给出处理口径。", 8)
	chunks := parseText(t, text, testConfig(4000, 0))
	if len(chunks) != 1 {
		t.Fatalf("expected the short paragraphs to merge, got %d chunk(s)", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "短句一。") || !strings.Contains(chunks[0].Content, "短句二。") {
		t.Errorf("merged chunk lost a short paragraph: %q", head(chunks[0].Content, 40))
	}
}

// A paragraph boundary is a semantic boundary: the next paragraph must not
// repeat the previous one's tail.
func TestParseStreamParagraphBoundaryDoesNotCopyPreviousParagraph(t *testing.T) {
	first := strings.Repeat("第一段说明了一个独立的业务规则并给出处理口径。", 8)
	second := strings.Repeat("第二段说明了一个独立的业务规则并给出处理口径。", 8)
	chunks := parseText(t, first+"\n\n"+second, testConfig(4000, 200))
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if strings.Contains(chunks[1].Content, "第一段") {
		t.Errorf("chunk 1 repeats the previous paragraph: %q", head(chunks[1].Content, 40))
	}
}
