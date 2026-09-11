package retrieval

import "testing"

func TestStabilizeRankingPrefersShortPolicyOverLongHub(t *testing.T) {
	query := "差旅费报销过程中，员工需要准备哪些附件？"
	hubContent := "爱因斯坦在柏林发表了广义相对论，这本书讲述了他的科学与人生。"
	candidates := []Candidate{
		{ChunkID: "hub-1", DocID: "einstein", Content: hubContent, Score: 0.0164},
		{ChunkID: "hub-2", DocID: "einstein", Content: hubContent + "续1", Score: 0.0162},
		{ChunkID: "hub-3", DocID: "einstein", Content: hubContent + "续2", Score: 0.0161},
		{ChunkID: "hub-4", DocID: "einstein", Content: hubContent + "续3", Score: 0.0160},
		{ChunkID: "hub-5", DocID: "einstein", Content: hubContent + "续4", Score: 0.0159},
		{ChunkID: "policy-1", DocID: "finance-policy", Content: "差旅费报销需准备发票、行程单等附件。", Score: 0.0158},
		{ChunkID: "other-1", DocID: "housing", Content: "公租房申请材料清单。", Score: 0.0150},
	}

	got := stabilizeRanking(query, candidates)
	selected, _ := diversifyCandidates(got, 5, 2)
	if selected[0].DocID != "finance-policy" {
		t.Fatalf("expected short policy first, got %+v", selected)
	}
	if selected[0].ChunkID != "policy-1" {
		t.Fatalf("expected policy chunk first, got %+v", selected)
	}
}

func TestStabilizeRankingKeepsSecondRequiredDocumentInTopK(t *testing.T) {
	query := "陈伟的OA账号和部门分别是什么？"
	candidates := []Candidate{
		{ChunkID: "noise-1", DocID: "noise", Content: "employee roster page 1", Score: 0.02},
		{ChunkID: "req-a", DocID: "required-a", Content: "陈伟 OA账号 chenwei", Score: 0.019},
		{ChunkID: "other-1", DocID: "other-1", Content: "unrelated policy handbook", Score: 0.018},
		{ChunkID: "noise-2", DocID: "noise", Content: "employee roster page 2", Score: 0.017},
		{ChunkID: "other-2", DocID: "other-2", Content: "another handbook chapter", Score: 0.016},
		{ChunkID: "req-b", DocID: "required-b", Content: "陈伟 部门 综合管理部", Score: 0.015},
	}

	got := stabilizeRanking(query, candidates)
	selected, stats := diversifyCandidates(got, 5, 2)
	docs := map[string]int{}
	ids := make([]string, len(selected))
	for i, candidate := range selected {
		ids[i] = candidate.ChunkID
		docs[candidate.DocID]++
	}
	if docs["required-a"] != 1 || docs["required-b"] != 1 {
		t.Fatalf("expected both required docs in top-5, got %v stats=%+v", ids, stats)
	}
}

func TestStabilizeRankingPreservesTiedOrder(t *testing.T) {
	query := "办公用品申请"
	candidates := []Candidate{
		{ChunkID: "a-1", DocID: "a", Content: "办公用品申请流程", Score: 0.01},
		{ChunkID: "b-1", DocID: "b", Content: "办公用品申请规则", Score: 0.01},
	}
	got := stabilizeRanking(query, candidates)
	if got[0].ChunkID != "a-1" || got[1].ChunkID != "b-1" {
		t.Fatalf("expected stable original order, got %+v", got)
	}
}

func TestStabilizeRankingDoesNotReorderOverlappingPolicies(t *testing.T) {
	query := "根据通知，哪些人员被授予了通报表扬？"
	candidates := []Candidate{
		{ChunkID: "a-1", DocID: "award-notice", Content: "关于给予有关人员即时激励的通知，通报表扬下列人员。", Score: 0.016},
		{ChunkID: "b-1", DocID: "excellent-notice", Content: "关于2018年度优秀评选活动的通知。", Score: 0.015},
		{ChunkID: "c-1", DocID: "secrecy-month", Content: "关于开展保密宣传月活动的通知。", Score: 0.014},
	}
	got := stabilizeRanking(query, candidates)
	if got[0].ChunkID != "a-1" || got[1].ChunkID != "b-1" || got[2].ChunkID != "c-1" {
		t.Fatalf("expected original overlapping order, got %+v", got)
	}
}

func TestLexicalOverlapMatchesYearLeaveParaphrase(t *testing.T) {
	query := lexicalTokens("员工年假天数怎么算？")
	content := lexicalTokens("在职人员年休假信息收集表")
	if lexicalOverlap(query, content) == 0 {
		t.Fatalf("expected 年假/年休假 paraphrase overlap, query=%v content=%v", query, content)
	}
}

func TestAttachCandidateFileNames(t *testing.T) {
	candidates := []Candidate{
		{DocID: "oa", Content: "陈伟", Metadata: map[string]string{"order_id": "1"}},
		{DocID: "leave", Content: "年假"},
		{DocID: "oa", Content: "duplicate already named", Metadata: map[string]string{"file_name": "keep.xls"}},
	}
	attachCandidateFileNames(candidates, map[string]string{
		"oa":    "OA账号.xls",
		"leave": "年休假信息收集表.xls",
	})
	if candidates[0].Metadata["file_name"] != "OA账号.xls" {
		t.Fatalf("expected oa filename, got %+v", candidates[0].Metadata)
	}
	if candidates[1].Metadata["file_name"] != "年休假信息收集表.xls" {
		t.Fatalf("expected leave filename, got %+v", candidates[1].Metadata)
	}
	if candidates[2].Metadata["file_name"] != "keep.xls" {
		t.Fatalf("expected existing filename to win, got %+v", candidates[2].Metadata)
	}
}
