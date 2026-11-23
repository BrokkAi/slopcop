package review

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

func TestWaitStoreTakesAFreeLockWithoutWaiting(t *testing.T) {
	root, _, held := reviewRepo(t)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	store, waited, err := WaitStore(context.Background(), root, time.Minute, func() { t.Error("waiting called for a free lock") })
	if err != nil || waited {
		t.Fatalf("waited %v, err %v", waited, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitStoreGetsTheLockWhenItIsReleased(t *testing.T) {
	root, _, held := reviewRepo(t)
	var calls atomic.Int32
	// releasing closes before the lock is released, so a waiter that holds
	// the lock must see it closed. closed lets the holder finish before the
	// fixture's cleanup closes the same store.
	releasing, closed := make(chan struct{}), make(chan struct{})
	waiting := func() {
		if calls.Add(1) == 1 {
			go func() {
				defer close(closed)
				time.Sleep(250 * time.Millisecond)
				close(releasing)
				_ = held.Close()
			}()
		}
	}
	store, waited, err := WaitStore(context.Background(), root, 15*time.Second, waiting)
	afterRelease := false
	select {
	case <-releasing:
		afterRelease = true
	default:
	}
	if calls.Load() > 0 {
		<-closed
	}
	if err != nil || !waited {
		t.Fatalf("waited %v, err %v", waited, err)
	}
	defer store.Close()
	if !afterRelease {
		t.Fatal("took the lock while another store held it")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("waiting called %d times", n)
	}
	if _, err := OpenStore(root); !errors.Is(err, ErrBusy) {
		t.Errorf("the waiter does not hold the lock: %v", err)
	}
}

func TestWaitStoreStopsAtTheLimit(t *testing.T) {
	root, _, _ := reviewRepo(t)
	start := time.Now()
	store, waited, err := WaitStore(context.Background(), root, 150*time.Millisecond, nil)
	if !errors.Is(err, ErrBusy) || store != nil || !waited {
		t.Fatalf("store %v, waited %v, err %v", store, waited, err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("gave up after %s", elapsed)
	}
}

func TestWaitStoreStopsWhenCanceled(t *testing.T) {
	root, _, _ := reviewRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	store, _, err := WaitStore(ctx, root, time.Minute, cancel)
	if !errors.Is(err, context.Canceled) || store != nil {
		t.Fatalf("store %v, err %v", store, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("stopped after %s", elapsed)
	}
}

// Each worktree has its own review state, so a check in one never waits for a
// check in another.
func TestWorktreesHaveSeparateReviewLocks(t *testing.T) {
	root, _, held := reviewRepo(t)
	defer held.Close()
	other := filepath.Join(t.TempDir(), "other")
	if _, err := gitx.Run(root, "worktree", "add", "-q", "--detach", other); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(other)
	if err != nil {
		t.Fatalf("a store in another worktree blocked this one: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
