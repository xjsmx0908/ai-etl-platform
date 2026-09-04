package releasecenter

import (
	"regexp"
	"strings"
)

// ContentChunk is the minimal content seam used by deterministic pre-review.
// The chunk ID is retained as an evidence reference; no full document payload
// is persisted in the review report.
type ContentChunk struct {
	ChunkID string
	Content string
}

type ContentReviewResult struct {
	Failed         bool
	Risk           RiskLevel
	Recommendation string
	Summary        string
	Findings       []Finding
}

var (
	secretPattern    = regexp.MustCompile(`(?i)(password|passwd|secret|api[_ -]?key)\s*[:=]`)
	idPattern        = regexp.MustCompile(`\b\d{17}[0-9Xx]\b`)
	phonePattern     = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	injectionPattern = regexp.MustCompile(`(?i)(忽略|无视|ignore)\s*(之前|先前|previous|prior)?\s*(的)?\s*(指令|instructions?|提示词|system prompt)`)
)

// AnalyzeContent applies bounded, deterministic content checks. It is an
// evidence producer, not a classifier: confidential content is escalated for
// human review, while prompt injection or exposed secrets block publication.
func AnalyzeContent(permission string, chunks []ContentChunk) ContentReviewResult {
	if len(chunks) == 0 {
		return ContentReviewResult{Failed: true, Risk: RiskHigh, Recommendation: "manual_review", Summary: "无法读取文档内容，已转人工复核"}
	}
	result := ContentReviewResult{Risk: RiskLow, Recommendation: "publish", Findings: []Finding{}}
	usableChunks := 0
	for _, chunk := range chunks {
		content := strings.TrimSpace(chunk.Content)
		if content == "" {
			continue
		}
		usableChunks++
		if injectionPattern.MatchString(content) {
			result.Risk = RiskHigh
			result.Recommendation = "needs_info"
			result.Findings = append(result.Findings, Finding{Code: "prompt_injection_detected", Severity: "high", Summary: "检测到疑似提示词注入内容", EvidenceRef: chunk.ChunkID})
			continue
		}
		if secretPattern.MatchString(content) || idPattern.MatchString(content) || phonePattern.MatchString(content) {
			if riskRank(result.Risk) < riskRank(RiskHigh) {
				result.Risk = RiskHigh
			}
			if strings.EqualFold(strings.TrimSpace(permission), "internal") {
				result.Recommendation = "needs_info"
			}
			result.Findings = append(result.Findings, Finding{Code: "sensitive_data_detected", Severity: "high", Summary: "检测到疑似敏感信息，需人工确认发布范围", EvidenceRef: chunk.ChunkID})
		}
	}
	if usableChunks == 0 {
		return ContentReviewResult{Failed: true, Risk: RiskHigh, Recommendation: "manual_review", Summary: "无法读取文档内容，已转人工复核"}
	}
	if len(result.Findings) > 0 {
		result.Summary = "内容检查发现 " + strings.TrimSpace(strings.Join(findingCodes(result.Findings), "、"))
	}
	return result
}

func riskRank(risk RiskLevel) int {
	switch risk {
	case RiskCritical:
		return 4
	case RiskHigh:
		return 3
	case RiskMedium:
		return 2
	case RiskLow:
		return 1
	default:
		return 0
	}
}

func findingCodes(findings []Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}
