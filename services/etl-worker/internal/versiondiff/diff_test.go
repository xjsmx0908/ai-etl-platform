package versiondiff

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// chunks builds a version from content hashes, one block per position.
func chunks(hashes ...string) []Chunk {
	built := make([]Chunk, len(hashes))
	for i, hash := range hashes {
		built[i] = Chunk{ChunkID: fmt.Sprintf("chunk-%d", i), Index: i, ContentHash: hash}
	}
	return built
}

// changeKinds flattens a change list for comparison. It returns nil rather than
// an empty slice so that "no changes" compares equal to the nil in the table
// above; reflect.DeepEqual treats the two as different.
func changeKinds(changes []Change) []Kind {
	if len(changes) == 0 {
		return nil
	}
	kinds := make([]Kind, len(changes))
	for i, change := range changes {
		kinds[i] = change.Kind
	}
	return kinds
}

// TestInsertionDoesNotReadAsARewrite is the reason this package aligns on
// content rather than on position. Comparing position by position reports the
// two shifted blocks as rewrites; they are unchanged text that moved, and a
// reviewer told "two paragraphs were rewritten" would go looking for edits that
// are not there.
func TestInsertionDoesNotReadAsARewrite(t *testing.T) {
	result, err := Compare(chunks("a", "b", "c"), chunks("a", "x", "b", "c"))
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	counts := result.Counts()
	if counts[KindModified] != 0 {
		t.Fatalf("an insertion reported %d modified block(s), want 0", counts[KindModified])
	}
	if counts[KindAdded] != 1 || counts[KindMoved] != 2 || result.Unchanged != 1 {
		t.Fatalf("got added=%d moved=%d unchanged=%d, want added=1 moved=2 unchanged=1",
			counts[KindAdded], counts[KindMoved], result.Unchanged)
	}
}

func TestCompareClassifiesEachKindOfEdit(t *testing.T) {
	tests := []struct {
		name      string
		previous  []Chunk
		current   []Chunk
		unchanged int
		want      []Kind
	}{
		{
			name:      "identical versions",
			previous:  chunks("a", "b"),
			current:   chunks("a", "b"),
			unchanged: 2,
			want:      nil,
		},
		{
			name:      "edited in place",
			previous:  chunks("a", "b", "c"),
			current:   chunks("a", "b2", "c"),
			unchanged: 2,
			want:      []Kind{KindModified},
		},
		{
			name:      "removed from the middle",
			previous:  chunks("a", "b", "c"),
			current:   chunks("a", "c"),
			unchanged: 1,
			want:      []Kind{KindMoved, KindRemoved},
		},
		{
			name:      "appended at the end",
			previous:  chunks("a"),
			current:   chunks("a", "b"),
			unchanged: 1,
			want:      []Kind{KindAdded},
		},
		{
			name:      "replaced wholesale",
			previous:  chunks("a", "b", "c"),
			current:   chunks("a", "x", "y"),
			unchanged: 1,
			want:      []Kind{KindModified, KindModified},
		},
		{
			name:      "both sides empty",
			previous:  nil,
			current:   nil,
			unchanged: 0,
			want:      nil,
		},
		{
			name:      "everything removed",
			previous:  chunks("a", "b"),
			current:   nil,
			unchanged: 0,
			want:      []Kind{KindRemoved, KindRemoved},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Compare(test.previous, test.current)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			if result.Unchanged != test.unchanged {
				t.Fatalf("unchanged = %d, want %d", result.Unchanged, test.unchanged)
			}
			if got := changeKinds(result.Changes); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("changes = %v, want %v", got, test.want)
			}
		})
	}
}

// TestMovedChangeNamesWhereTheBlockCameFrom keeps the move report usable: saying
// a block moved without saying from where leaves the reader to diff the
// positions themselves.
func TestMovedChangeNamesWhereTheBlockCameFrom(t *testing.T) {
	result, err := Compare(chunks("a", "b", "c"), chunks("a", "x", "b", "c"))
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	for _, change := range result.Changes {
		if change.Kind != KindMoved {
			continue
		}
		if change.Index == change.PreviousIndex {
			t.Fatalf("moved block reported at the same position %d it came from", change.Index)
		}
	}
	moved := 0
	for _, change := range result.Changes {
		if change.Kind == KindMoved {
			moved++
		}
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2", moved)
	}
}

// TestCompareIsReproducible guards the property the package is named for: the
// same pair of versions produces the same answer, whatever order the caller
// happens to hold the blocks in.
func TestCompareIsReproducible(t *testing.T) {
	previous := chunks("a", "b", "c", "d", "e")
	current := chunks("a", "c", "x", "d", "e", "f")
	want, err := Compare(previous, current)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		again, err := Compare(previous, current)
		if err != nil {
			t.Fatalf("Compare: %v", err)
		}
		if !reflect.DeepEqual(again, want) {
			t.Fatalf("run %d disagreed with the first run:\n got %+v\nwant %+v", attempt, again, want)
		}
	}

	reordered := []Chunk{previous[3], previous[0], previous[4], previous[1], previous[2]}
	fromReordered, err := Compare(reordered, current)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !reflect.DeepEqual(fromReordered, want) {
		t.Fatalf("reordering the input changed the answer:\n got %+v\nwant %+v", fromReordered, want)
	}
}

func TestCompareRejectsChunksItCannotPlace(t *testing.T) {
	valid := chunks("a", "b")

	duplicate := []Chunk{
		{ChunkID: "x", Index: 0, ContentHash: "a"},
		{ChunkID: "y", Index: 0, ContentHash: "b"},
	}
	if _, err := Compare(valid, duplicate); !errors.Is(err, ErrDuplicateIndex) {
		t.Fatalf("duplicate position: err = %v, want ErrDuplicateIndex", err)
	}

	missingHash := []Chunk{{ChunkID: "x", Index: 0}}
	if _, err := Compare(valid, missingHash); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("missing content hash: err = %v, want ErrInvalidChunk", err)
	}

	missingID := []Chunk{{Index: 0, ContentHash: "a"}}
	if _, err := Compare(valid, missingID); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("missing chunk id: err = %v, want ErrInvalidChunk", err)
	}

	negative := []Chunk{{ChunkID: "x", Index: -1, ContentHash: "a"}}
	if _, err := Compare(valid, negative); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("negative position: err = %v, want ErrInvalidChunk", err)
	}
}

func TestCompareRefusesAPairTooLargeToAlign(t *testing.T) {
	side := make([]Chunk, 2001)
	for i := range side {
		side[i] = Chunk{ChunkID: fmt.Sprintf("chunk-%d", i), Index: i, ContentHash: fmt.Sprintf("hash-%d", i)}
	}
	if _, err := Compare(side, side); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// TestCompareAcceptsAPairAtTheLimit is the control for the test above: without
// it, a Compare that rejected every large input would still pass.
func TestCompareAcceptsAPairAtTheLimit(t *testing.T) {
	side := make([]Chunk, 2000)
	for i := range side {
		side[i] = Chunk{ChunkID: fmt.Sprintf("chunk-%d", i), Index: i, ContentHash: fmt.Sprintf("hash-%d", i)}
	}
	if _, err := Compare(side, side); err != nil {
		t.Fatalf("Compare at the limit: %v", err)
	}
}
