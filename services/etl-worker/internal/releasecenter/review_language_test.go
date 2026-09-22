package releasecenter

import "testing"

// Every case below is either an identifier a Chinese field legitimately carries
// or a value copied verbatim out of release_center_reviews. The live values are
// the point: the first version of this predicate counted English function words
// and was tuned on the two sentences at the bottom, which left it blind to the
// two shapes that were already on the panel -- the failure template and the
// English noun phrase.
func TestLooksLikeEnglishThresholds(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},

		{"chinese", "材料不适合进入当前知识空间", false},
		{"bare identifier", "space_fit", false},
		{"identifier in chinese", "space_fit 判定为 uncertain", false},
		{"identifiers in chinese", "将 space_fit 记为 uncertain、knowledge_usable 记为 usable", false},
		{"product name in chinese", "OpenTelemetry Collector 配置需要人工确认", false},
		{"label with a product name", "办公区门禁管理制度，行政制度类文档，但存在字符损坏与内容不完整", false},
		{"label with a han marker", "受管空间测试标记片段（PX-MANAGED-V2），内容为单句补贴标准，过于简短，疑似存根/测试文档", false},
		{"label with a replacement char", "门禁管理制度；主体内容完整，但第二块含乱码替换字符（U+FFFD），疑似编码损坏", false},
		{"short chinese label", "制度", false},

		{"english sentence", "The material does not belong in this knowledge space", true},
		{"english sentence with an identifier", "Test/placeholder marker string with a gate passphrase; not an approved HR, admin", true},
		{"failure template", `agent review returned status "failed"`, true},
		{"english noun phrase", "Draft/placeholder marker text, not a formal policy document", true},
		{"english label", "Payroll policy knowledge document in demo managed knowledge space", true},
		{"english label with a parenthetical", "payroll policy knowledge content (Chinese)", true},
		{"english label, no function word", "placeholder/replacement marker stub, not substantive policy content", true},
		{"truncated english label", "placeholder/draft stub mentioning a gate password and a unique marker; not actua", true},
	}
	for _, tc := range cases {
		if got := LooksLikeEnglish(tc.text); got != tc.want {
			t.Errorf("%s: LooksLikeEnglish(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

// The English paragraph quotes Chinese prose, so "carries no Han" cannot be the
// only evidence that a field is English. This is the summary of
// review-6055e3dd7aa52258, trimmed at the same place the panel shows it.
func TestLooksLikeEnglishCatchesEnglishThatQuotesChinese(t *testing.T) {
	text := "Candidate chunk doc-1788958054422977825_0000 contains only a test/placeholder marker " +
		"string ('受管替换闭环第二版…门禁口令是橙门') rather than an approved HR, administrative, or " +
		"business regulation for the production knowledge space."
	if !LooksLikeEnglish(text) {
		t.Fatalf("an English sentence that quotes Chinese must still read as English: %q", text)
	}
}

// The English strings below are copied verbatim from review-6055e3dd7aa52258,
// the verdict that put an English paragraph on the review panel.
func TestNormalizeReviewLanguageRewritesEnglishProse(t *testing.T) {
	in := ReviewLanguage{
		Recommendation: "needs_info",
		KindLabel:      "Test/placeholder marker string with a gate passphrase; not an approved HR, admin",
		Summary: "Candidate chunk doc-1788958054422977825_0000 contains only a test/placeholder marker " +
			"string ('受管替换闭环第二版…门禁口令是橙门') rather than an approved HR, administrative, or " +
			"business regulation for the production knowledge space. Knowledge-fitness assessment " +
			"recorded space_fit=mismatch and knowledge_usable=not_knowledge, so the material cannot be " +
			"published as formal knowledge.",
		Findings: []Finding{
			{Code: "space_mismatch", Severity: "medium", Summary: "The material does not belong in this knowledge space", EvidenceRef: "doc-1788958054422977825_0000"},
			{Code: "planner_invented_this", Severity: "medium", Summary: "This is a sentence the planner wrote on its own", EvidenceRef: "doc-1788958054422977825_0000"},
		},
	}
	got := NormalizeReviewLanguage(in)
	if got.KindLabel != "" {
		t.Fatalf("an English kind_label must be dropped, got %q", got.KindLabel)
	}
	if got.Summary != "预审未通过，需补充材料或人工确认后重审，共 2 项待确认问题。" {
		t.Fatalf("summary must be rebuilt from the recommendation, got %q", got.Summary)
	}
	if got.Findings[0].Summary != "不适合本空间" {
		t.Fatalf("a known code must take its Chinese name, got %q", got.Findings[0].Summary)
	}
	if got.Findings[1].Summary != unlabeledFindingSummary {
		t.Fatalf("an unknown code must fall back to the generic Chinese note, got %q", got.Findings[1].Summary)
	}
	// A finding's identity is not a translation. The code, the severity and the
	// evidence ref have to survive the rewrite, or the panel loses the link to the
	// chunk the verdict rests on.
	if got.Findings[0].Code != "space_mismatch" || got.Findings[0].Severity != "medium" ||
		got.Findings[0].EvidenceRef != "doc-1788958054422977825_0000" {
		t.Fatalf("normalization must not touch code, severity or evidence: %+v", got.Findings[0])
	}
}

// The Chinese report below is copied verbatim from review-91847c6aa8e77c605020b37e2099f6d2,
// the sibling verdict that came back in Chinese under the same prompt version. It
// carries English identifiers, which is exactly what must not read as English.
func TestNormalizeReviewLanguageKeepsChineseUntouched(t *testing.T) {
	in := ReviewLanguage{
		Recommendation: "needs_info",
		KindLabel:      "制度",
		Summary: "演示入职文档内容完整、无敏感数据、无提示注入风险；但知识适配评估将 space_fit 判定为 " +
			"uncertain（无法确认材料是否适合当前演示知识空间），按规则该状态不得发布，故需人工确认" +
			"材料归属后再决定是否发布。",
		Findings: []Finding{
			{Code: "space_fit_uncertain", Severity: "medium", Summary: "无法确认材料是否适合当前知识空间", EvidenceRef: "demo-doc-onboarding-0"},
		},
	}
	got := NormalizeReviewLanguage(in)
	if got.KindLabel != in.KindLabel || got.Summary != in.Summary {
		t.Fatalf("a Chinese report must pass through unchanged: %+v", got)
	}
	if got.Findings[0].Summary != in.Findings[0].Summary {
		t.Fatalf("a Chinese finding must pass through unchanged: %+v", got.Findings[0])
	}
}

// The stored row below is copied verbatim from review-d6559154618ac1cf, the
// verdict the review panel was showing when the defect was reported: an
// autonomous-review-v2 row written before the language contract was enforced.
// Its findings were already Chinese, so the read path only has to fix the two
// prose fields -- and it has to leave the findings, which carry the reason a
// reviewer needs, exactly as the deterministic scan wrote them.
func TestNormalizeReviewReportLocalizesStoredEnglishVerdict(t *testing.T) {
	report := ReviewReport{
		ID:             "review-d6559154618ac1cf",
		Status:         "completed",
		Recommendation: "needs_info",
		RiskLevel:      RiskMedium,
		PromptVersion:  "autonomous-review-v2",
		Summary: "Exact candidate is a single draft/placeholder marker chunk containing only " +
			"'受管闭环干净第一版。唯一标记 PXGAP-MGC1-20260909-P7Q2。本版门禁口令是青门。' with no actual " +
			"policy content. Knowledge-space fitness assessment determined the material does not fit " +
			"the production knowledge space (space_fit=mismatch) and cannot be used as formal knowledge " +
			"(knowledge_usable=not_knowledge). Per policy, publication must not proceed when space_fit is " +
			"mismatch or knowledge_usable is not usable; additional information or a proper approved " +
			"document is required.",
		KindLabel: "Draft/placeholder marker text, not a formal policy document",
		Findings: []Finding{
			{Code: "space_mismatch", Severity: "medium", Summary: "材料不适合进入当前知识空间", EvidenceRef: "doc-1788958407926440244_0000"},
			{Code: "not_knowledge", Severity: "medium", Summary: "材料不能作为正式知识使用", EvidenceRef: "doc-1788958407926440244_0000"},
		},
	}
	NormalizeReviewReport(&report)

	if LooksLikeEnglish(report.Summary) {
		t.Fatalf("a stored English summary must not reach the reviewer: %q", report.Summary)
	}
	if report.Summary != "预审未通过，需补充材料或人工确认后重审，共 2 项待确认问题。" {
		t.Fatalf("summary must be rebuilt from the recommendation, got %q", report.Summary)
	}
	if report.KindLabel != "" {
		t.Fatalf("a stored English kind_label must be dropped, got %q", report.KindLabel)
	}
	if report.Findings[0].Summary != "材料不适合进入当前知识空间" ||
		report.Findings[1].Summary != "材料不能作为正式知识使用" {
		t.Fatalf("the deterministic Chinese findings must survive: %+v", report.Findings)
	}
	// Serving a localised verdict must not rewrite what the verdict *says*.
	if report.Status != "completed" || report.Recommendation != "needs_info" || report.RiskLevel != RiskMedium {
		t.Fatalf("normalization must not touch the structured verdict: %+v", report)
	}
}

// The failure template is English too, and it is the whole summary: the
// coordinator used to overwrite whatever the agent recorded with
// `agent review returned status "failed"`, which says nothing a reviewer did not
// already know from 预审状态：预审失败. Normalizing it yields the sentence that
// actually tells the reviewer what happens next.
func TestNormalizeReviewReportLocalizesFailureTemplate(t *testing.T) {
	report := ReviewReport{
		Status:         "failed",
		Recommendation: "manual_review",
		RiskLevel:      RiskHigh,
		Summary:        `agent review returned status "failed"`,
	}
	NormalizeReviewReport(&report)
	if report.Summary != "预审未给出结论，已转人工复核。" {
		t.Fatalf("a failed verdict must say what happens next, got %q", report.Summary)
	}
}
