package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

func TestFilteredCaptureDiffAndReuseScope(t *testing.T) {
	root, original, store := reviewRepo(t)
	if err := os.WriteFile(filepath.Join(root, "large.json"), []byte(strings.Repeat("j", 500001)), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := gitx.CaptureWorkingTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := gitx.FilterCheckTree(root, original.BaseCommit, snapshot.Tree)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := NewFilteredCapture(root, original.BaseCommit, snapshot.Tree, filtered.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if capture.Empty() || capture.Tree != snapshot.Tree || strings.Contains(capture.Numstat, "large.json") || !strings.Contains(capture.Numstat, "change.txt") {
		t.Fatalf("%+v", capture)
	}
	options := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter("clean", "gpt-6-terra", "")}}}
	first := Run(context.Background(), store, capture, options)
	second := Run(context.Background(), store, capture, options)
	if first.Status != StatusClean || second.Status != StatusClean || !second.Reused {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	// Include the previously omitted content under the same full tree. The
	// narrower review must not be reused for this broader scope.
	full, err := NewCapture(root, original.BaseCommit, snapshot.Tree)
	if err != nil {
		t.Fatal(err)
	}
	third := Run(context.Background(), store, full, options)
	if third.Status != StatusClean || third.Reused {
		t.Fatalf("broader scope reused old review: %+v", third)
	}
	if !(Capture{BaseTree: filtered.Tree, Tree: snapshot.Tree, DiffTree: filtered.Tree}).Empty() {
		t.Fatal("empty filtered scope treated as changed")
	}
}
