package releasecenter

import "strings"
import "testing"

func TestEvaluateKnowledgeFitnessAllowsPublishWhenPurposeMissing(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		KnowledgeUsable: KnowledgeUseUsable,
		KindLabel:       "会议纪要",
		EvidenceRefs:    []string{"chunk-1"},
		ChunkIDs:        []string{"chunk-1"},
	})
	if result.Configured || result.SpaceFit != "" || result.KnowledgeUsable != KnowledgeUseUsable || result.Recommendation != "publish" || result.Risk != RiskLow || len(result.Findings) != 0 {
		t.Fatalf("unconfigured space should not block: %+v", result)
	}
	if result.KindLabel != "会议纪要" {
		t.Fatalf("kind label was dropped: %+v", result)
	}
}

func TestEvaluateKnowledgeFitnessIgnoresKindLabelAsPublishSwitch(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		Purpose:         "只放已生效的人事制度",
		SpaceFit:        SpaceFitMatch,
		KnowledgeUsable: KnowledgeUseUsable,
		KindLabel:       "其他：出差报销流程图",
		EvidenceRefs:    []string{"chunk-1"},
		ChunkIDs:        []string{"chunk-1"},
	})
	if result.Recommendation != "publish" || len(result.Findings) != 0 || result.KindLabel != "其他：出差报销流程图" {
		t.Fatalf("kind label must not block publish: %+v", result)
	}
}

func TestEvaluateKnowledgeFitnessRequiresMatchWhenPurposeConfigured(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		Purpose:         "只放已生效的人事制度",
		KnowledgeUsable: KnowledgeUseUsable,
		EvidenceRefs:    []string{"chunk-1"},
		ChunkIDs:        []string{"chunk-1"},
	})
	if result.SpaceFit != SpaceFitUncertain || result.Recommendation != "needs_info" || len(result.Findings) != 1 || result.Findings[0].Code != spaceFitUncertainCode {
		t.Fatalf("configured space without a fit judgment should be uncertain: %+v", result)
	}
}

func TestEvaluateKnowledgeFitnessBlocksMismatchAndNotKnowledge(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		Purpose:         "只放已生效的人事制度",
		SpaceFit:        SpaceFitMismatch,
		KnowledgeUsable: KnowledgeUseNotKnowledge,
		EvidenceRefs:    []string{"chunk-9"},
		ChunkIDs:        []string{"chunk-9"},
	})
	if result.Recommendation != "needs_info" || result.Risk != RiskMedium || len(result.Findings) != 2 {
		t.Fatalf("mismatch and non-knowledge should both floor: %+v", result)
	}
	codes := findingCodes(result.Findings)
	if strings.Join(codes, ",") != spaceMismatchCode+","+notKnowledgeCode {
		t.Fatalf("unexpected findings: %v", codes)
	}
	if result.Findings[0].EvidenceRef != "chunk-9" {
		t.Fatalf("missing evidence: %+v", result.Findings)
	}
}

func TestEvaluateKnowledgeFitnessBlocksIncompleteKnowledgeWithoutPurpose(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		KnowledgeUsable: KnowledgeUseIncomplete,
		EvidenceRefs:    []string{"chunk-2"},
		ChunkIDs:        []string{"chunk-2"},
	})
	if result.Configured || result.Recommendation != "needs_info" || len(result.Findings) != 1 || result.Findings[0].Code != incompleteKnowledgeCode {
		t.Fatalf("incomplete knowledge should block even without purpose: %+v", result)
	}
}

func TestEvaluateKnowledgeFitnessRejectsUnknownEvidence(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		Purpose:         "制度库",
		SpaceFit:        SpaceFitMatch,
		KnowledgeUsable: KnowledgeUseUsable,
		EvidenceRefs:    []string{"old-chunk"},
		ChunkIDs:        []string{"chunk-1"},
	})
	if result.Recommendation != "needs_info" || len(result.Findings) != 1 || result.Findings[0].Code != fitnessEvidenceMissingCode {
		t.Fatalf("unknown evidence must not be rewritten onto another chunk: %+v", result)
	}
}

func TestEvaluateKnowledgeFitnessFailsClosedWithoutChunkEvidence(t *testing.T) {
	result := EvaluateKnowledgeFitness(FitnessInput{
		Purpose:         "制度库",
		SpaceFit:        SpaceFitMatch,
		KnowledgeUsable: KnowledgeUseUsable,
	})
	if result.Recommendation != "needs_info" || len(result.Findings) != 1 || result.Findings[0].Code != fitnessEvidenceMissingCode {
		t.Fatalf("missing evidence was not blocked: %+v", result)
	}
}
