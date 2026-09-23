// Package versiondiff computes the deterministic block-level difference between
// two indexed versions of the same document.
//
// Deterministic is the point. The result is a function of the two chunk lists
// alone, so two runs over the same inputs agree and a reviewer can recompute it
// from the projections without trusting this code. Nothing here calls a model --
// that is what separates this from the release centre's agent pre-review, which
// produces a judgement; this produces a fact.
package versiondiff

import (
	"errors"
	"fmt"
	"sort"
)

var (
	// ErrInvalidChunk reports a chunk that cannot be positioned or identified, so
	// the comparison would silently drop or misplace it.
	ErrInvalidChunk = errors.New("versiondiff: invalid chunk")
	// ErrDuplicateIndex reports two chunks claiming the same position. Alignment
	// assumes one block per position, and a document that violates that is not
	// something a diff should paper over.
	ErrDuplicateIndex = errors.New("versiondiff: duplicate chunk index")
	// ErrTooLarge reports a pair of versions whose alignment table would not fit
	// in a sane amount of memory.
	ErrTooLarge = errors.New("versiondiff: version pair is too large to align")
)

// maxAlignmentArea bounds the dynamic-programming table (len(previous) x
// len(current)). At the limit the table is 4M ints; a document anywhere near
// that is already far past what a reviewer reads, so refusing is better than
// allocating first and failing later.
const maxAlignmentArea = 4_000_000

// Chunk is one indexed block, identified the way both projections already store
// it. Content itself is deliberately absent: what changed is decided by
// ContentHash, and carrying the text here would invite comparing it instead.
type Chunk struct {
	ChunkID     string
	Index       int
	ContentHash string
}

// Kind is what became of one block between the two versions.
type Kind string

const (
	// KindAdded: the block exists only in the new version.
	KindAdded Kind = "added"
	// KindRemoved: the block exists only in the old version.
	KindRemoved Kind = "removed"
	// KindModified: the block still occupies the same position but its content
	// hash changed.
	KindModified Kind = "modified"
	// KindMoved: the content is unchanged but now sits at a different position,
	// which is what an insertion or deletion above it looks like.
	KindMoved Kind = "moved"
)

// Change is one block whose fate is not "unchanged".
type Change struct {
	Kind          Kind
	ChunkID       string
	ContentHash   string
	Index         int // position in the new version; for a removal, the old position
	PreviousIndex int // where the block used to sit, for KindMoved
}

// Result is the difference between two versions. Unchanged is a count rather
// than a list because the interesting answer to "what changed" is the changes,
// and a caller that needs the unchanged blocks already has both versions.
type Result struct {
	PreviousChunks int
	CurrentChunks  int
	Unchanged      int
	Changes        []Change
}

// Counts returns how many changes of each kind the result holds.
func (r Result) Counts() map[Kind]int {
	counts := make(map[Kind]int, 4)
	for _, change := range r.Changes {
		counts[change.Kind]++
	}
	return counts
}

// Compare reports what changed going from previous to current.
//
// The two versions are aligned on content hash rather than on position, because
// aligning on position turns a single inserted paragraph into "everything below
// it was rewritten". Once aligned, a block that disappeared and a block that
// appeared at the same position is reported as one modification instead of a
// removal plus an addition.
func Compare(previous, current []Chunk) (Result, error) {
	if err := validate("previous", previous); err != nil {
		return Result{}, err
	}
	if err := validate("current", current); err != nil {
		return Result{}, err
	}
	if len(previous)*len(current) > maxAlignmentArea {
		return Result{}, fmt.Errorf("%w: %d x %d blocks", ErrTooLarge, len(previous), len(current))
	}
	// Alignment walks both slices in order, so the caller's slice order would
	// otherwise change the pairing for the same document.
	previous = sortedByIndex(previous)
	current = sortedByIndex(current)

	previousHashes := make([]string, len(previous))
	for i, chunk := range previous {
		previousHashes[i] = chunk.ContentHash
	}
	currentHashes := make([]string, len(current))
	for i, chunk := range current {
		currentHashes[i] = chunk.ContentHash
	}

	result := Result{PreviousChunks: len(previous), CurrentChunks: len(current)}
	alignedPrevious := make([]bool, len(previous))
	alignedCurrent := make([]bool, len(current))
	for _, pair := range lcsPairs(previousHashes, currentHashes) {
		oldIndex, newIndex := pair[0], pair[1]
		alignedPrevious[oldIndex] = true
		alignedCurrent[newIndex] = true
		if previous[oldIndex].Index == current[newIndex].Index {
			result.Unchanged++
			continue
		}
		result.Changes = append(result.Changes, Change{
			Kind:          KindMoved,
			ChunkID:       current[newIndex].ChunkID,
			ContentHash:   current[newIndex].ContentHash,
			Index:         current[newIndex].Index,
			PreviousIndex: previous[oldIndex].Index,
		})
	}

	// Whatever the alignment did not account for, keyed by position so the two
	// sides can be paired below.
	unmatchedPrevious := positionsByIndex(previous, alignedPrevious)
	unmatchedCurrent := positionsByIndex(current, alignedCurrent)

	for _, position := range sortedPositions(unmatchedCurrent) {
		if _, stillThere := unmatchedPrevious[position]; !stillThere {
			continue
		}
		newSlice := unmatchedCurrent[position]
		result.Changes = append(result.Changes, Change{
			Kind:        KindModified,
			ChunkID:     current[newSlice].ChunkID,
			ContentHash: current[newSlice].ContentHash,
			Index:       position,
		})
		delete(unmatchedPrevious, position)
		delete(unmatchedCurrent, position)
	}

	for _, position := range sortedPositions(unmatchedPrevious) {
		chunk := previous[unmatchedPrevious[position]]
		result.Changes = append(result.Changes, Change{
			Kind:        KindRemoved,
			ChunkID:     chunk.ChunkID,
			ContentHash: chunk.ContentHash,
			Index:       chunk.Index,
		})
	}
	for _, position := range sortedPositions(unmatchedCurrent) {
		chunk := current[unmatchedCurrent[position]]
		result.Changes = append(result.Changes, Change{
			Kind:        KindAdded,
			ChunkID:     chunk.ChunkID,
			ContentHash: chunk.ContentHash,
			Index:       chunk.Index,
		})
	}

	// Changes are emitted from three different sources above; sorting them makes
	// the output a function of the inputs rather than of map iteration order.
	sort.SliceStable(result.Changes, func(i, j int) bool {
		if result.Changes[i].Index != result.Changes[j].Index {
			return result.Changes[i].Index < result.Changes[j].Index
		}
		return result.Changes[i].Kind < result.Changes[j].Kind
	})
	return result, nil
}

func validate(side string, chunks []Chunk) error {
	seen := make(map[int]struct{}, len(chunks))
	for _, chunk := range chunks {
		if chunk.ChunkID == "" || chunk.ContentHash == "" || chunk.Index < 0 {
			return fmt.Errorf("%w: %s chunk %q at index %d is incomplete",
				ErrInvalidChunk, side, chunk.ChunkID, chunk.Index)
		}
		if _, duplicate := seen[chunk.Index]; duplicate {
			return fmt.Errorf("%w: %s has two chunks at index %d", ErrDuplicateIndex, side, chunk.Index)
		}
		seen[chunk.Index] = struct{}{}
	}
	return nil
}

// positionsByIndex maps a position to the slice offset holding it, skipping the
// chunks the alignment already accounted for.
func positionsByIndex(chunks []Chunk, aligned []bool) map[int]int {
	positions := make(map[int]int, len(chunks))
	for i, chunk := range chunks {
		if !aligned[i] {
			positions[chunk.Index] = i
		}
	}
	return positions
}

// sortedByIndex returns a copy ordered by position, so that the caller's slice
// order cannot change the answer.
func sortedByIndex(chunks []Chunk) []Chunk {
	ordered := make([]Chunk, len(chunks))
	copy(ordered, chunks)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	return ordered
}

func sortedPositions(positions map[int]int) []int {
	keys := make([]int, 0, len(positions))
	for position := range positions {
		keys = append(keys, position)
	}
	sort.Ints(keys)
	return keys
}

// lcsPairs returns, in order, the (previous, current) offsets of one longest
// common subsequence of the two hash sequences.
//
// The tie-break is fixed -- when both directions are equally good, advance the
// previous side -- so the pairs are a function of the inputs. A different but
// equally long subsequence would be just as correct and would not be
// reproducible, and reproducibility is the reason this package exists.
func lcsPairs(previous, current []string) [][2]int {
	rows, columns := len(previous), len(current)
	lengths := make([][]int, rows+1)
	for i := range lengths {
		lengths[i] = make([]int, columns+1)
	}
	for i := rows - 1; i >= 0; i-- {
		for j := columns - 1; j >= 0; j-- {
			if previous[i] == current[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
				continue
			}
			if lengths[i+1][j] >= lengths[i][j+1] {
				lengths[i][j] = lengths[i+1][j]
			} else {
				lengths[i][j] = lengths[i][j+1]
			}
		}
	}

	var pairs [][2]int
	for i, j := 0, 0; i < rows && j < columns; {
		switch {
		case previous[i] == current[j]:
			pairs = append(pairs, [2]int{i, j})
			i, j = i+1, j+1
		case lengths[i+1][j] >= lengths[i][j+1]:
			i++
		default:
			j++
		}
	}
	return pairs
}
