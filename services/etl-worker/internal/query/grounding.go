package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// maxGroundingContextChars caps how much retrieved context is sent to the
// verifier so a long context cannot inflate the second LLM call.
const maxGroundingContextChars = 8000

// groundingMaxTokens budgets the verifier's completion. The verdict itself is
// ~10 tokens, but reasoning models spend their budget on internal reasoning
// tokens first and only emit content afterwards. Too tight a cap makes them stop
// mid-reasoning and return an EMPTY content string, which surfaces as "no
// supported verdict in verifier response" and — because the gate fails open —
// silently disables the check on every query. Keep enough headroom for the
// reasoning pass, and do not lower this to "just fit the JSON".
const groundingMaxTokens = 512

// groundingSystemPrompt instructs the verifier to judge whether an answer's
// claims are traceable to the retrieved documents and whether it answers the
// user's actual question. It is deliberately strict about numbers and novel
// facts, and lenient about paraphrase and translation.
const groundingSystemPrompt = `你是严格的回答校验器。分别判断：
1. 「回答」中的关键断言（事实、数字、专有名词、具体结论）是否都能在「参考文档」中找到明确支持；
2. 「回答」是否直接回答了「用户问题」，而不是只陈述相关但不同的事实。
严禁使用你对世界的常识——即使回答在常识上合理，只要文档中没有明确出现，就必须判 false。
判定规则：
- 文档中明确存在该事实或数字 → supported=true
- 回答是对文档的忠实概括或翻译，且不新增文档外信息 → supported=true
- 回答包含文档中没有的数字、事实或结论（哪怕看似合理）→ supported=false
- 回答基于常识补充了文档没有的内容 → supported=false
- 回答解决了问题所询问的对象和事项 → answers_question=true
- 回答只提供背景、相邻制度或其他对象的信息 → answers_question=false
示例：
问题：「每天最多查询多少次？」 文档：「系统每天最多查询 200 次」 回答：「每天最多可查询 200 次」→ {"supported":true,"answers_question":true}
问题：「谁可以审批上传？」 文档：「支持 PDF 格式上传」 回答：「支持 PDF 格式上传」→ {"supported":true,"answers_question":false}
问题：「每天最多查询多少次？」 文档：「系统每天最多查询 200 次」 回答：「查询上限是 500 次」→ {"supported":false,"answers_question":true}
只输出 JSON：{"supported": true, "answers_question": true}，两个字段都必须出现。`

// groundingCheck asks a verifier model whether answer is supported by sources.
// It returns (true, nil) when supported, (false, nil) when the answer contains
// claims not traceable to the sources, and an error when the verifier itself
// fails (callers decide how to handle a failed verification).
func (s *Service) groundingCheck(ctx context.Context, question, answer string, sources []SourceContext) (bool, error) {
	var contextBuilder bytes.Buffer
	for _, src := range sources {
		fmt.Fprintf(&contextBuilder, "<document doc_id=%q>\n%s\n</document>\n\n", src.DocID, src.Content)
	}
	docs := contextBuilder.String()
	if len(docs) > maxGroundingContextChars {
		docs = docs[:maxGroundingContextChars]
	}

	messages := []map[string]string{
		{"role": "system", "content": groundingSystemPrompt},
		{"role": "user", "content": fmt.Sprintf(
			"参考文档：\n%s\n\n用户问题：%s\n\n回答：%s",
			docs, question, answer,
		)},
	}
	reqBody := map[string]interface{}{
		"model":       s.llmModel,
		"messages":    messages,
		"max_tokens":  groundingMaxTokens,
		"temperature": 0,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return false, fmt.Errorf("marshal grounding prompt: %w", err)
	}
	result, err := s.callLLM(ctx, data)
	if err != nil {
		return false, err
	}
	// Name truncation explicitly. An empty content with finish_reason="length"
	// means the budget was consumed before the verdict was emitted, which calls
	// for raising groundingMaxTokens — not for rewriting the prompt.
	if strings.TrimSpace(result.Content) == "" && result.FinishReason == "length" {
		return false, fmt.Errorf("verifier truncated before emitting a verdict (finish_reason=length, max_tokens=%d, completion_tokens=%d)",
			groundingMaxTokens, result.CompletionTokens)
	}
	return parseGroundingVerdict(result.Content)
}

// parseGroundingVerdict extracts both required booleans from a verifier response
// that may include markdown fences or surrounding prose. A passing verdict
// requires evidence support and a direct answer to the user's question.
func parseGroundingVerdict(content string) (bool, error) {
	supportedMatch := regexp.MustCompile(`(?i)"supported"\s*:\s*(true|false)`).FindStringSubmatch(content)
	answersQuestionMatch := regexp.MustCompile(`(?i)"answers_question"\s*:\s*(true|false)`).FindStringSubmatch(content)
	if len(supportedMatch) != 2 || len(answersQuestionMatch) != 2 {
		return false, fmt.Errorf("incomplete answer-verification verdict: %.200s", content)
	}
	return strings.EqualFold(supportedMatch[1], "true") &&
		strings.EqualFold(answersQuestionMatch[1], "true"), nil
}
