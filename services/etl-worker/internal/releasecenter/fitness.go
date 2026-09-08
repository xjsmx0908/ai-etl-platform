package releasecenter

import "strings"

const (
	SpaceFitMatch     = "match"
	SpaceFitMismatch  = "mismatch"
	SpaceFitUncertain = "uncertain"

	KnowledgeUseUsable       = "usable"
	KnowledgeUseNotKnowledge = "not_knowledge"
	KnowledgeUseIncomplete   = "incomplete"

	spaceMismatchCode          = "space_mismatch"
	spaceFitUncertainCode      = "space_fit_uncertain"
	notKnowledgeCode           = "not_knowledge"
	incompleteKnowledgeCode    = "incomplete_knowledge"
	fitnessEvidenceMissingCode = "fitness_evidence_missing"
)

type FitnessInput struct {
	Purpose         string
	SpaceFit        string
	KnowledgeUsable string
	KindLabel       string
	EvidenceRefs    []string
	ChunkIDs        []string
}

type FitnessResult struct {
	SpaceFit        string
	KnowledgeUsable string
	KindLabel       string
	EvidenceRefs    []string
	Configured      bool
	Risk            RiskLevel
	Recommendation  string
	Findings        []Finding
}

// EvaluateKnowledgeFitness records whether a document belongs in this knowledge
// space and can be used as formal knowledge. Material type labels are optional
// notes for humans and never a publish switch. Matching is only configured
// when the space owner has written a purpose.
func EvaluateKnowledgeFitness(in FitnessInput) FitnessResult {
	result := FitnessResult{
		KindLabel:       clipRunes(in.KindLabel, 80),
		Risk:            RiskLow,
		Recommendation:  "publish",
		Findings:        []Finding{},
		EvidenceRefs:    validFitnessEvidence(in.EvidenceRefs, in.ChunkIDs),
		Configured:      strings.TrimSpace(in.Purpose) != "",
		KnowledgeUsable: normalizeKnowledgeUse(in.KnowledgeUsable),
	}
	if result.KnowledgeUsable == "" {
		result.KnowledgeUsable = KnowledgeUseUsable
	}
	if result.Configured {
		result.SpaceFit = normalizeSpaceFit(in.SpaceFit)
		if result.SpaceFit == "" {
			result.SpaceFit = SpaceFitUncertain
		}
	}

	evidenceRef := ""
	if len(result.EvidenceRefs) > 0 {
		evidenceRef = result.EvidenceRefs[0]
	} else if len(in.EvidenceRefs) == 0 && len(in.ChunkIDs) > 0 {
		evidenceRef = strings.TrimSpace(in.ChunkIDs[0])
		if evidenceRef != "" {
			result.EvidenceRefs = []string{evidenceRef}
		}
	}

	block := func(code, summary string) {
		ref := evidenceRef
		if strings.TrimSpace(ref) == "" {
			code = fitnessEvidenceMissingCode
			summary = "适合性判断缺少原文依据"
		}
		result.Findings = append(result.Findings, Finding{Code: code, Severity: "medium", Summary: summary, EvidenceRef: ref})
		result.Recommendation = "needs_info"
		if riskRank(result.Risk) < riskRank(RiskMedium) {
			result.Risk = RiskMedium
		}
	}

	if strings.TrimSpace(evidenceRef) == "" {
		block(fitnessEvidenceMissingCode, "适合性判断缺少原文依据")
		return result
	}
	if result.Configured {
		switch result.SpaceFit {
		case SpaceFitMismatch:
			block(spaceMismatchCode, "材料不适合进入当前知识空间")
		case SpaceFitUncertain:
			block(spaceFitUncertainCode, "无法确认材料是否适合当前知识空间")
		}
	}
	switch result.KnowledgeUsable {
	case KnowledgeUseNotKnowledge:
		block(notKnowledgeCode, "材料不能作为正式知识使用")
	case KnowledgeUseIncomplete:
		block(incompleteKnowledgeCode, "材料不完整，无法作为正式知识发布")
	}
	return result
}

func normalizeSpaceFit(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SpaceFitMatch, SpaceFitMismatch, SpaceFitUncertain:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeKnowledgeUse(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case KnowledgeUseUsable, KnowledgeUseNotKnowledge, KnowledgeUseIncomplete:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func validFitnessEvidence(refs, chunkIDs []string) []string {
	allowed := map[string]bool{}
	for _, chunkID := range chunkIDs {
		chunkID = strings.TrimSpace(chunkID)
		if chunkID != "" {
			allowed[chunkID] = true
		}
	}
	out := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || !allowed[ref] || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

func clipRunes(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
