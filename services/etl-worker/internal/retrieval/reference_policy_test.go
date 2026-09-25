package retrieval

import "testing"

// The two questions below the marker list were the measured failure: both were
// answered from an arbitrarily chosen candidate with `refusal_reason` unset.
// Everything in the negative table is a question the corpus can answer, and a
// gate that refuses any of them is worse than the guessing it replaces — which
// is why they are pinned here rather than only the positives.
func TestHasUnresolvedReference(t *testing.T) {
	tests := []struct {
		name     string
		question string
		want     bool
	}{
		// Measured on the deployed stack before the gate existed.
		{"measured handbook guess", "刚才那份文档里还写了什么？", true},
		{"measured second-point guess", "上面说的第二点是什么意思？", true},

		{"just-now that document", "刚才那个标准是多少？", true},
		{"just-now what was said", "刚才说的报销比例是多少？", true},
		{"above that document", "上面那份规范里怎么规定的？", true},
		{"above what was mentioned", "上面提到的密码要求是什么？", true},
		{"earlier what was said", "之前说的住宿标准是多少？", true},
		{"last answer", "上一条回答里的金额是多少？", true},
		{"last turn", "上一轮问的那个流程是什么？", true},
		{"space inside the marker", "刚才 那份文档里还写了什么？", true},
		{"full-width space inside the marker", "上面\u3000说的第二点是什么意思？", true},

		// Answerable questions. Refusing these would be a regression.
		{"named document", "员工手册里还写了什么？", false},
		{"temporal 之前", "入职之前需要准备什么材料？", false},
		{"temporal 之前 with 的", "试用期之前的规定是什么？", false},
		{"just-now upload is a different question", "刚才上传的文档多久可以检索到？", false},
		{"pasted passage makes 上述 resolvable", "上述内容里报销标准是多少？", false},
		{"plain business question", "住宿费标准是多少？", false},
		{"ordinal inside a named document", "合同第三条规定了什么？", false},
		{"bare ordinal is a known boundary", "第二点是什么意思？", false},
		{"identifier question", "合同编号 CT-9999-0001 的违约金比例是多少？", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasUnresolvedReference(tc.question); got != tc.want {
				t.Fatalf("HasUnresolvedReference(%q) = %v, want %v", tc.question, got, tc.want)
			}
		})
	}
}

// A marker split across a newline is pasted text, not a question about a
// previous turn, so it must not fire. This is what keeps stripSpaces narrow.
func TestHasUnresolvedReferenceDoesNotJoinAcrossNewlines(t *testing.T) {
	question := "制度摘录：\n刚才\n那份文档"
	if HasUnresolvedReference(question) {
		t.Fatalf("HasUnresolvedReference(%q) = true, want false", question)
	}
}

// The marker list is the whole contract. A silently emptied slice would make
// every positive case fail, but a silently widened one would only show up as a
// refused question in production — so pin the two measured phrases by name.
func TestUnresolvedReferenceMarkersCoverTheMeasuredPhrases(t *testing.T) {
	for _, required := range []string{"刚才那份", "上面说的"} {
		found := false
		for _, marker := range unresolvedReferenceMarkers {
			if marker == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("marker %q is missing from the policy", required)
		}
	}
}
