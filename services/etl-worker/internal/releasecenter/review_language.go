package releasecenter

import (
	"fmt"
	"strings"
	"unicode"
)

// ReviewLanguage is the reviewer-facing subset of a verdict.
//
// It exists because the contract below has to hold on both sides of the
// platform: the review adapter enforces it on the verdict it is about to store,
// and the API enforces it on the verdict it is about to serve. Go has no
// structural typing and AgentReview and ReviewReport share no embedded type, so
// the fields are projected onto this shape and the rule is written once against
// the projection. A second copy of the rule is exactly the drift this type
// exists to prevent.
type ReviewLanguage struct {
	Recommendation string
	Summary        string
	KindLabel      string
	Findings       []Finding
}

// reviewFindingCodeLabels names the finding codes the platform emits. It mirrors
// the web panel's FINDING_LABELS for the same reason: a code is an identifier,
// and an identifier must never reach a reviewer's screen.
var reviewFindingCodeLabels = map[string]string{
	"sensitive_data_detected":   "敏感信息",
	"prompt_injection_detected": "提示词注入",
	"space_mismatch":            "不适合本空间",
	"space_fit_uncertain":       "是否适合本空间看不准",
	"not_knowledge":             "不能作为正式知识",
	"incomplete_knowledge":      "材料不完整",
	"fitness_evidence_missing":  "缺少适合性证据",
	"insufficient_evidence":     "材料内容不足",
}

const (
	unlabeledFindingSummary = "预审发现问题，需人工确认"
	undecidedReviewSummary  = "预审已完成，请人工确认"
	// failedReviewHead is the verdict half of a failed review. It is one string
	// rather than two so the sentence a reviewer reads does not depend on which
	// of the two code paths happened to write the row: reviewSummaryFor uses it
	// for a manual_review verdict, and FailureSummary uses it for the failure
	// path.
	failedReviewHead = "预审未给出结论，已转人工复核"
	// failureReasonLimit bounds the diagnostic that FailureSummary appends. A
	// provider error body is machine text of unknown length -- the observed 503
	// body is ~250 characters -- and the panel shows it inline.
	failureReasonLimit = 200
)

// NormalizeReviewLanguage enforces Chinese on every field a reviewer reads.
//
// Asking for Chinese in the prompt is not the same as enforcing it. Under one
// prompt version the same model wrote an English summary for
// doc-1788958054422977825 and a Chinese one for demo-doc-onboarding, so the
// language of a given verdict was whatever the model happened to pick. The
// requirement is therefore applied to the assembled report:
//
//   - kind_label decides nothing, so an English one is dropped and the panel
//     falls back to "未标注（不影响发布）"
//   - summary is rebuilt from the report's own enums, which carry no language
//   - a finding keeps its identity (code, severity, evidence_ref) but takes the
//     Chinese name of its code instead of an English sentence
func NormalizeReviewLanguage(in ReviewLanguage) ReviewLanguage {
	if LooksLikeEnglish(in.KindLabel) {
		in.KindLabel = ""
	}
	if LooksLikeEnglish(in.Summary) {
		in.Summary = reviewSummaryFor(in)
	}
	// Rewrite a copy, not the caller's slice. The stored verdict is the audit
	// record of what the model said, and localising it for one response must not
	// edit it in place for whoever holds the same backing array.
	if len(in.Findings) > 0 {
		findings := make([]Finding, len(in.Findings))
		copy(findings, in.Findings)
		in.Findings = findings
	}
	for i := range in.Findings {
		if !LooksLikeEnglish(in.Findings[i].Summary) {
			continue
		}
		label, ok := reviewFindingCodeLabels[strings.ToLower(strings.TrimSpace(in.Findings[i].Code))]
		if !ok {
			label = unlabeledFindingSummary
		}
		in.Findings[i].Summary = label
	}
	return in
}

// NormalizeAgentReview applies the contract to the verdict the review adapter is
// about to store.
func NormalizeAgentReview(review *AgentReview) {
	out := NormalizeReviewLanguage(ReviewLanguage{
		Recommendation: review.Recommendation,
		Summary:        review.Summary,
		KindLabel:      review.KindLabel,
		Findings:       review.Findings,
	})
	review.Summary, review.KindLabel, review.Findings = out.Summary, out.KindLabel, out.Findings
}

// NormalizeReviewReport applies the contract to a verdict that is about to be
// served to a reviewer.
//
// The write path enforces the contract on every verdict the current code
// produces, which is not the same as every verdict a reviewer can open. The
// language was only ever *requested* before the contract existed, so the rows
// already in the table kept whatever the model chose: autonomous-review-v2 left
// eight English summaries and eight English kind_labels behind, and those rows
// are the ones a reviewer opens today. Serving stored text verbatim therefore
// keeps a contract violation on screen for as long as the row lives. Applying
// the rule on the way out is what makes the panel Chinese independently of when
// the row was written.
func NormalizeReviewReport(report *ReviewReport) {
	out := NormalizeReviewLanguage(ReviewLanguage{
		Recommendation: report.Recommendation,
		Summary:        report.Summary,
		KindLabel:      report.KindLabel,
		Findings:       report.Findings,
	})
	report.Summary, report.KindLabel, report.Findings = out.Summary, out.KindLabel, out.Findings
}

// FailureSummary writes the summary of a failed review.
//
// Two requirements meet on this one field, and satisfying either alone produced
// a bad verdict:
//
//   - The reviewer's own words have to survive. The review adapter already
//     explains itself before it returns: agentapi/service.go stores the planner's
//     error in Summary (`Summary: err.Error()`, `Summary: run.Error`,
//     `Summary: "invalid review report: " + err.Error()`). The coordinator used to
//     overwrite that with its own sentence, so the only copy of the reason lived
//     in the agent run -- which expires with AGENT_RUN_TTL (24h) and is not
//     reachable from the review panel. review-a26fb4d1d170c359 and
//     review-69e42208b72c0e0b both read `agent review returned status "failed"`
//     and nothing else, and why they were written is gone.
//   - The text has to be Chinese. The failure path is the one write path that
//     never passed NormalizeReviewLanguage -- validateAutonomousReview is reached
//     only once the run completed and its report parsed -- and that is how an
//     English template reached the table in the first place.
//
// So the reason is kept verbatim rather than paraphrased (a summary of a machine
// error is worth less than the error) and bounded (see failureReasonLimit).
func FailureSummary(upstream string, cause error) string {
	if cause == nil {
		return failedReviewHead + "。"
	}
	reason := strings.TrimSpace(upstream)
	// An adapter that failed without saying anything leaves Summary empty or
	// equal to the coordinator's own cause; appending either would just repeat
	// the frame.
	if reason == "" || reason == cause.Error() {
		return failedReviewHead + "。"
	}
	return failedReviewHead + "。原因：" + truncateRunes(reason, failureReasonLimit)
}

// truncateRunes cuts on a rune boundary. The reason mixes ASCII JSON with Chinese
// prose, so a byte slice would split a character and store invalid UTF-8.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// reviewSummaryFor writes the one-line verdict from the report's structured
// fields. Every input is a system enum, so the result cannot inherit the model's
// language choice. It replaces a paragraph the model wrote with the sentence a
// reviewer actually needs: can this be published, and if not, why not.
func reviewSummaryFor(in ReviewLanguage) string {
	head := map[string]string{
		"publish":       "预审通过，未发现阻断发布的问题",
		"needs_info":    "预审未通过，需补充材料或人工确认后重审",
		"reject":        "预审未通过，不建议发布",
		"manual_review": failedReviewHead,
	}[strings.ToLower(strings.TrimSpace(in.Recommendation))]
	if head == "" {
		head = undecidedReviewSummary
	}
	if len(in.Findings) == 0 {
		return head + "。"
	}
	return fmt.Sprintf("%s，共 %d 项待确认问题。", head, len(in.Findings))
}

// LooksLikeEnglish reports whether a reviewer-facing field is English rather
// than Chinese.
//
// Two shapes count, and both are in the table today:
//
//   - a sentence: six run-on ASCII words, at least two of them function words.
//     This is what catches an English paragraph that quotes Chinese prose --
//     the review-6055e3dd7aa52258 summary embeds 受管替换闭环第二版 inside its
//     own English sentence, so "carries no Han" alone would not fire on it.
//   - a phrase: no Han character at all, spread over more than one word. This is
//     what catches the two shapes the sentence test misses, and both of them
//     reached the review panel in English: the failure template
//     `agent review returned status "failed"` (five words, not one function
//     word) and the English noun phrase `Draft/placeholder marker text, not a
//     formal policy document` (eight words, exactly one function word).
//
// A single Han-free word is left alone: one token is as likely to be an
// identifier ("space_fit", "PXGAP-MGC1") as it is to be English.
//
// Neither shape fires on a field that *opens* in Chinese -- see englishSentence.
// The predicate asks what a reviewer reads, and a reviewer reads the opening:
// every English value that reached the panel opened in English
// (`Exact candidate is ...`, `All required review steps ...`,
// `Draft/placeholder marker text, ...`) and every Chinese one opened in Chinese.
// A field that opens with a Chinese verdict and then quotes machine text is read
// in Chinese -- the failure summary is exactly that shape (FailureSummary), and
// judging it on its whole content would replace it with the bare verdict and
// throw the reason away again.
func LooksLikeEnglish(text string) bool {
	return englishSentence(text) || hanFreePhrase(text)
}

// hanFreePhrase reports whether text carries no Han character across more than
// one word. A Chinese field always carries Han, even when it quotes English
// identifiers, so the absence of Han is the evidence and the identifiers are
// not.
func hanFreePhrase(text string) bool {
	words := 0
	for _, word := range strings.Fields(text) {
		// Every word has to be checked before deciding: one Han word anywhere in
		// the field makes the whole field Chinese, however many English
		// identifiers sit in front of it.
		if containsHan(word) {
			return false
		}
		if len(word) >= 2 {
			words++
		}
	}
	return words > 1
}

func containsHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// englishFunctionWords are the words a Chinese sentence does not borrow. A
// Chinese summary legitimately contains English identifiers -- space_fit,
// uncertain, OpenTelemetry -- because those are the names of things, so counting
// ASCII words alone would misfire on exactly the text this fix has to preserve.
// Function words are what separates English prose from Chinese prose with
// identifiers in it.
var englishFunctionWords = map[string]bool{
	"the": true, "and": true, "or": true, "not": true, "but": true,
	"than": true, "rather": true, "that": true, "this": true, "these": true,
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"has": true, "have": true, "been": true, "cannot": true, "must": true,
	"to": true, "of": true, "in": true, "on": true, "at": true,
	"by": true, "from": true, "with": true, "for": true, "as": true,
}

// englishSentence reports whether text reads as English sentences rather than
// Chinese carrying a few English identifiers. Three conditions must hold: the
// field opens in Chinese-free text, has six run-on ASCII words, and at least two
// of them are function words. An English sentence always clears all three;
// "space_fit 判定为 uncertain" clears none of the last two.
//
// The opening matters because a quoted diagnostic is not prose. The failure
// summary opens with 预审未给出结论，已转人工复核。and then quotes the provider's
// error verbatim; the quoted body alone would clear both word counts, so without
// this test the contract would rewrite the whole field back into the bare verdict
// and the reason would be lost a second time, in a new place.
func englishSentence(text string) bool {
	if startsWithHan(text) {
		return false
	}
	words, functionWords := 0, 0
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z')
	}) {
		if len(word) < 2 {
			continue
		}
		words++
		if englishFunctionWords[strings.ToLower(word)] {
			functionWords++
		}
	}
	return words >= 6 && functionWords >= 2
}

// startsWithHan reports whether the first character that is not whitespace or
// punctuation is Han. Leading quotes and brackets are skipped so a field wrapped
// in them is judged by its opening word.
func startsWithHan(text string) bool {
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		return unicode.Is(unicode.Han, r)
	}
	return false
}
