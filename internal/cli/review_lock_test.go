package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

const reviewLockWaitLine = "waiting up to"

// watchedWriter is a stderr a test can read while the check still writes to
// it. seen closes when the text first appears.
type watchedWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	want   string
	seen   chan struct{}
	once   sync.Once
}

func newWatchedWriter(want string) *watchedWriter {
	return &watchedWriter{want: want, seen: make(chan struct{})}
}

func (w *watchedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	if strings.Contains(w.buffer.String(), w.want) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *watchedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// lockedCheck is a repository with a change and a working fake reviewer whose
// review store another process holds, and a service counting the checks sent.
type lockedCheck struct {
	repo   string
	server *httptest.Server
	holder *review.Store
	sent   atomic.Int32
	patch  atomic.Value
}

func newLockedCheck(t *testing.T) *lockedCheck {
	t.Helper()
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	c := &lockedCheck{repo: newRepo(t, "https://github.com/BrokkAi/slopcop.git")}
	writeFile(t, c.repo, "README.md", "changed\n")
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/check" {
			c.sent.Add(1)
			var body struct {
				Change string `json:"change"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				if patch, err := base64.StdEncoding.DecodeString(body.Change); err == nil {
					c.patch.Store(string(patch))
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"clean","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0,"incompletePolicies":0,"failOn":"finding"},"findings":[],"incomplete":[]}`)
	}))
	t.Cleanup(c.server.Close)
	holder, err := review.OpenStore(c.repo)
	if err != nil {
		t.Fatal(err)
	}
	c.holder = holder
	t.Cleanup(func() { _ = c.holder.Close() })
	return c
}

type checkResult struct {
	code   int
	stdout string
}

// start runs a check in the background, as a second process would.
func (c *lockedCheck) start(ctx context.Context, stderr *watchedWriter, args ...string) <-chan checkResult {
	done := make(chan checkResult, 1)
	args = append([]string{"check", "--root", c.repo, "--service", c.server.URL, "--format", "json"}, args...)
	go func() {
		var stdout bytes.Buffer
		code := execute(ctx, args, "test", &stdout, stderr)
		done <- checkResult{code, stdout.String()}
	}()
	return done
}

// awaitWaiting returns once the check says it is waiting for the lock.
func awaitWaiting(t *testing.T, stderr *watchedWriter, done <-chan checkResult) {
	t.Helper()
	select {
	case <-stderr.seen:
	case result := <-done:
		t.Fatalf("check ended without waiting: exit %d, stdout %s, stderr %s", result.code, result.stdout, stderr.String())
	case <-time.After(15 * time.Second):
		t.Fatalf("check did not start waiting: %s", stderr.String())
	}
}

func awaitResult(t *testing.T, stderr *watchedWriter, done <-chan checkResult) checkResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(30 * time.Second):
		t.Fatalf("check did not end: %s", stderr.String())
		return checkResult{}
	}
}

func TestCheckWaitsForTheReviewLockAndChecksTheTreeThenPresent(t *testing.T) {
	c := newLockedCheck(t)
	stderr := newWatchedWriter(reviewLockWaitLine)
	done := c.start(context.Background(), stderr)
	awaitWaiting(t, stderr, done)
	if sent := c.sent.Load(); sent != 0 {
		t.Fatalf("%d static checks sent while waiting", sent)
	}
	// What the holder was doing may change the checkout; the check covers
	// what is there once it is this check's turn.
	writeFile(t, c.repo, "README.md", "changed while waiting\n")
	if err := c.holder.Close(); err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, stderr, done)
	if result.code != service.ExitClean {
		t.Fatalf("exit %d: stdout %s stderr %s", result.code, result.stdout, stderr.String())
	}
	var parsed struct {
		Status   string `json:"status"`
		Approved bool   `json:"approved"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &parsed); err != nil {
		t.Fatalf("stdout is not JSON: %v: %s", err, result.stdout)
	}
	if parsed.Status != "clean" || !parsed.Approved {
		t.Errorf("report = %+v", parsed)
	}
	if lines := strings.Count(stderr.String(), reviewLockWaitLine); lines != 1 {
		t.Errorf("%d wait lines in %s", lines, stderr.String())
	}
	if sent := c.sent.Load(); sent != 1 {
		t.Errorf("%d static checks sent", sent)
	}
	if patch, _ := c.patch.Load().(string); !strings.Contains(patch, "changed while waiting") {
		t.Errorf("the check sent the tree from before the wait: %s", patch)
	}
	snapshot, err := gitx.CaptureWorkingTree(c.repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if approved, err := review.Reviewed(c.repo, snapshot.Tree); err != nil || !approved {
		t.Errorf("the tree present after the wait is not approved: %v, %v", approved, err)
	}
}

func TestCheckGivesUpOnTheReviewLockWithoutSpendingACheck(t *testing.T) {
	c := newLockedCheck(t)
	snapshot, err := gitx.CaptureWorkingTree(c.repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The holder may be about to approve this very tree, or already has: a
	// check that never got to review it has no say in that.
	if err := review.MarkReviewed(c.repo, snapshot.Tree); err != nil {
		t.Fatal(err)
	}
	stderr := newWatchedWriter(reviewLockWaitLine)
	result := awaitResult(t, stderr, c.start(context.Background(), stderr, "--review-timeout", "150ms"))
	if result.code != service.ExitIncomplete {
		t.Fatalf("exit %d: stdout %s stderr %s", result.code, result.stdout, stderr.String())
	}
	var failure struct {
		Status    string `json:"status"`
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &failure); err != nil {
		t.Fatalf("stdout is not JSON: %v: %s", err, result.stdout)
	}
	if failure.Status != "failed" || failure.Code != "review_busy" || !failure.Retryable {
		t.Errorf("failure = %+v", failure)
	}
	if sent := c.sent.Load(); sent != 0 {
		t.Errorf("%d static checks spent on a check that could not review", sent)
	}
	if approved, err := review.Reviewed(c.repo, snapshot.Tree); err != nil || !approved {
		t.Errorf("giving up withdrew the tree's approval: %v, %v", approved, err)
	}
}

func TestCheckStopsPromptlyWhileWaitingForTheReviewLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal syscall.Signal
		code   int
	}{
		{"interrupt", syscall.SIGINT, 130},
		{"terminate", syscall.SIGTERM, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newLockedCheck(t)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			stderr := newWatchedWriter(reviewLockWaitLine)
			done := c.start(ctx, stderr)
			awaitWaiting(t, stderr, done)
			cancel(interrupt{tc.signal})
			select {
			case result := <-done:
				if result.code != tc.code || result.stdout != "" {
					t.Errorf("exit %d, stdout %q", result.code, result.stdout)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("check did not stop promptly")
			}
			if !strings.Contains(stderr.String(), interruptedBeforeSendText) {
				t.Errorf("stderr = %s", stderr.String())
			}
			if sent := c.sent.Load(); sent != 0 {
				t.Errorf("%d static checks sent", sent)
			}
		})
	}
}

// An override accepts the review on record, which a running check is about to
// replace, so it refuses at once rather than wait and accept the new one
// unread.
func TestOverrideDoesNotWaitForTheReviewLock(t *testing.T) {
	c := newLockedCheck(t)
	snapshot, err := gitx.CaptureWorkingTree(c.repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, _, stderr := runCLI(t, "override", "--root", c.repo, "--reason", "wrong")
	if code == 0 || !strings.Contains(stderr, "nothing was accepted") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("override waited %s", elapsed)
	}
	if approved, err := review.Reviewed(c.repo, snapshot.Tree); err != nil || approved {
		t.Errorf("override approved the tree: %v, %v", approved, err)
	}
}
