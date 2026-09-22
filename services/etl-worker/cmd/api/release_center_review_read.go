package main

import (
	"context"

	"ai-etl-pipeline/internal/releasecenter"
)

// reviewReader is the shape every handler needs to read a stored verdict. It is
// deliberately narrower than releasecenter.Store: the review read path must not
// be able to reach the write methods.
type reviewReader interface {
	GetReview(context.Context, string, string) (releasecenter.ReviewReport, error)
}

// readReview is the only way the API reads a stored verdict.
//
// The language contract is applied here rather than at the call sites, for the
// reason the contract exists at all: the stored text is whatever the model wrote
// on the day, and the prompt only ever *asks* for Chinese. Verdicts written
// before the contract was enforced are still in the table -- the review panel
// showed a 524-character English paragraph for review-d6559154618ac1cf while a
// sibling row written under the same prompt version came back in Chinese -- and
// a reviewer opens those rows today. Applying the rule on the way out is what
// keeps the panel Chinese independently of when the row was written, and a
// single seam is what keeps a handler added later from serving the raw text.
func readReview(ctx context.Context, store reviewReader, tenantID, reviewID string) (releasecenter.ReviewReport, error) {
	report, err := store.GetReview(ctx, tenantID, reviewID)
	if err != nil {
		return report, err
	}
	releasecenter.NormalizeReviewReport(&report)
	return report, nil
}
