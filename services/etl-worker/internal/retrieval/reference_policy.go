package retrieval

import "strings"

// unresolvedReferenceMarkers are phrases that point at a previous turn.
//
// The question endpoint keeps no conversation state, so a question whose
// referent lives in an earlier turn cannot be answered at all — the referent is
// simply absent. Left ungated, the answering path picks whichever retrieved
// candidate looks closest and answers from it. The citations stay faithful, so
// the grounding verifier passes, and the caller receives a confident answer to
// a question nobody asked: the failure is in the premise, and no amount of
// citation checking can see it. Measured on the deployed stack before this
// gate existed, "刚才那份文档里还写了什么？" was answered out of the employee
// handbook and "上面说的第二点是什么意思？" out of the security policy, both
// with `refusal_reason` unset.
//
// The list holds whole phrases rather than composing a "backward pointer" with
// a "reference noun" at runtime, because the precision of this gate is what
// matters: refusing a question the corpus can answer is worse than the guessing
// it replaces. Each exclusion below is a question that is answerable, and so is
// deliberately not a marker:
//
//   - 「刚才上传的文档多久能查到」 asks about the ingestion policy, not about a
//     previous answer — so bare 「刚才」 / 「刚刚」 is not a marker.
//   - 「入职之前需要准备什么材料」 is answerable — so bare 「之前」 is not one.
//   - 「上述内容」 is not one either: a caller may paste a passage into the
//     question, which makes "the above" resolvable without any history.
//
// Known boundary: a bare ordinal back-reference ("第二点是什么意思？") is not
// detected, because it cannot be told apart from an in-document reference
// ("合同第三条是什么？") without knowing whether a document was named. Missing
// it leaves the old guessing behaviour in place for that one shape; catching it
// wrongly would refuse an answerable question.
var unresolvedReferenceMarkers = []string{
	// 刚才 / 刚刚 + a reference to the thing that was shown or said.
	"刚才那份", "刚才那个", "刚才那条", "刚才那段", "刚才这", "刚才说的", "刚才讲的",
	"刚才问的", "刚才提的", "刚才提到的", "刚才回答的", "刚才答的",
	"刚刚那份", "刚刚那个", "刚刚说的", "刚刚提到的",
	// 上面 / 前面 + the same.
	"上面那份", "上面那个", "上面那条", "上面这段", "上面说的", "上面讲的", "上面问的",
	"上面提的", "上面提到的",
	"前面那份", "前面那个", "前面那条", "前面说的", "前面讲的", "前面提到的",
	// 之前 + the same.
	"之前那份", "之前那个", "之前说的", "之前提到的", "之前问的", "之前讲的",
	// Explicit turn references.
	"上次那份", "上次那个", "上次说的", "上次提到的",
	"上一条", "上一个问题", "上一个回答", "上一轮", "上一次回答", "上一步说的",
}

// HasUnresolvedReference reports whether the question refers back to a previous
// turn, which this endpoint cannot resolve because it keeps no conversation
// state.
//
// Whitespace is stripped before matching: a caller typing 「刚才 那份文档」 means
// the same thing as 「刚才那份文档」, and the markers are Chinese phrases that
// contain no meaningful internal space.
func HasUnresolvedReference(question string) bool {
	normalized := strings.TrimSpace(question)
	if normalized == "" {
		return false
	}
	normalized = stripSpaces(normalized)
	for _, marker := range unresolvedReferenceMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// stripSpaces removes ASCII and full-width spaces. Newlines and tabs are left
// alone: they only ever appear in pasted text, where a marker spanning them is
// not a marker.
func stripSpaces(s string) string {
	if !strings.ContainsAny(s, " \u3000") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' || r == '\u3000' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
