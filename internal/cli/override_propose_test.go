package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/review"
)

// gateHook runs the Claude Code gate hook on a shell command run in dir,
// and returns its stderr when it blocked the command.
func gateHook(t *testing.T, dir, command string) (bool, string) {
	t.Helper()
	cmd := newGateCommand()
	payload, _ := json.Marshal(map[string]any{"cwd": dir, "tool_name": "Bash", "tool_input": map[string]string{"command": command}})
	cmd.SetIn(bytes.NewReader(payload))
	cmd.SetArgs([]string{"--hook", "claude"})
	var errOut bytes.Buffer
	cmd.SetErr(&errOut)
	return cmd.Execute() != nil, errOut.String()
}

func TestProposeThenAcceptAnOverride(t *testing.T) {
	isolateUserConfig(t)
	repo, id := reviewedRepo(t)
	tree := runGit(t, repo, "rev-parse", "HEAD^{tree}")

	if code, _, stderr := runCLI(t, "override", "--root", repo, "--propose"); code == 0 || !strings.Contains(stderr, "--reason is required") {
		t.Fatalf("propose without a reason: exit = %d, stderr = %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "override", "--root", repo, "--propose", "--reason", "an older proposal"); code != 0 {
		t.Fatalf("first propose: %s", stderr)
	}
	code, stdout, stderr := runCLI(t, "override", "--root", repo, "--propose", "--reason", "the npm job needs goreleaser")
	if code != 0 || !strings.Contains(stdout, "nothing is approved until a person accepts it") ||
		!strings.Contains(stdout, "ask the user to accept it by running `slopcop override --accept --root ") {
		t.Fatalf("propose: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if ok, _ := review.ReviewedWithoutStatic(repo, tree); ok {
		t.Fatal("a proposal approved the tree")
	}

	// The gate names the proposal and refuses the agent's own acceptance.
	blocked, reason := gateHook(t, repo, "git push")
	if !blocked || !strings.Contains(reason, "waits for the user (reason: the npm job needs goreleaser)") ||
		!strings.Contains(reason, "`! slopcop override --accept`") {
		t.Fatalf("gate on push: blocked = %v, reason = %q", blocked, reason)
	}
	// A proposal approves nothing, so it does not let a push after it through.
	if blocked, _ := gateHook(t, repo, `slopcop override --propose --reason "wrong" && git push`); !blocked {
		t.Error("a push after a proposal was let through")
	}
	for _, command := range []string{
		"slopcop override --accept",
		"cd sub && slopcop override --root . --accept",
		"slopcop override --accept=true && git push",
		"slopcop override '--accept'",
		`slopcop override --acc"ept"`,
		"slopcop override \\\n  --accept",
		"slopcop --accept=true override",
		"./bin/slopcop override --root . --accept",
		"slopcop override --acc\\\nept",
		`slopcop override --root "/tmp/repo;copy" --accept`,
		"slopcop override --root '/tmp/repo (copy)' --accept",
		"slopcop.exe override --accept",
		"slopcop override --accept; git push",
		"slopcop override --accept&& git push",
		"(slopcop override --accept)",
		"bash -lc 'slopcop override --accept'",
		"slopcop $'override' $'--accept'",
		"python -m brokk_slopcop override --accept",
	} {
		if blocked, reason := gateHook(t, repo, command); !blocked || !strings.Contains(reason, "only the user accepts a proposed override") {
			t.Errorf("%s: blocked = %v, reason = %q", command, blocked, reason)
		}
	}
	for _, command := range []string{
		`slopcop override --propose --reason "wrong"`,
		`slopcop override --propose --reason "the --accept flag is unrelated"`,
		`slopcop override --propose --reason "--accept=true is only in a fixture"`,
		"slopcop override --pending",
		`git commit -m "slopcop override --accept-all is not a flag"`,
	} {
		if blocked, reason := gateHook(t, repo, command); blocked {
			t.Errorf("%s was blocked: %q", command, reason)
		}
	}

	code, stdout, _ = runCLI(t, "override", "--root", repo, "--pending")
	if code != 0 || !strings.Contains(stdout, "for tree "+tree+" (proposed ") || !strings.Contains(stdout, "1 findings)\n  Reason: the npm job needs goreleaser\n") ||
		!strings.Contains(stdout, "slopcop: accept the latest with `slopcop override --accept --root ") {
		t.Fatalf("pending: exit = %d, stdout = %q", code, stdout)
	}
	code, stdout, _ = runCLI(t, "override", "--root", repo, "--pending", "--format", "json")
	var doc struct {
		Pending []review.Proposal `json:"pending"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); code != 0 || err != nil || len(doc.Pending) != 2 ||
		doc.Pending[0].Tree != tree || len(doc.Pending[0].Findings) != 1 || doc.Pending[0].Findings[0] != id {
		t.Fatalf("pending json: %v\n%s", err, stdout)
	}

	if code, _, stderr = runCLI(t, "override", "--root", repo, "--accept", "--reason", "mine"); code == 0 || !strings.Contains(stderr, "--accept takes the proposal's reason") {
		t.Fatalf("accept with a reason: exit = %d, stderr = %q", code, stderr)
	}
	code, stdout, stderr = runCLI(t, "override", "--root", repo, "--accept")
	if code != 0 || !strings.Contains(stdout, "slopcop: accepted proposed override "+doc.Pending[0].ID) ||
		!strings.Contains(stdout, "accepted the review's findings for tree "+tree) {
		t.Fatalf("accept: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if ok, _ := review.ReviewedWithoutStatic(repo, tree); !ok {
		t.Fatal("accepting the proposal did not approve the tree")
	}
	if blocked, reason := gateHook(t, repo, "git push"); blocked {
		t.Fatalf("push after accept was blocked: %q", reason)
	}
	store, err := review.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.Last()
	store.Close()
	if err != nil || last.Accepted == nil || last.Accepted.Reason != "the npm job needs goreleaser" ||
		last.Accepted.Proposal == nil || last.Accepted.Proposal.ID != doc.Pending[0].ID {
		t.Fatalf("last.json does not record the accepted proposal: %+v, %v", last, err)
	}

	if code, stdout, _ = runCLI(t, "override", "--root", repo, "--pending"); code != 0 || stdout != "slopcop: no proposed override waits for a person in this clone\n" {
		t.Fatalf("pending after accept: exit = %d, stdout = %q", code, stdout)
	}
	if code, _, stderr = runCLI(t, "override", "--root", repo, "--accept"); code == 0 || !strings.Contains(stderr, "no override is proposed") {
		t.Fatalf("second accept: exit = %d, stderr = %q", code, stderr)
	}
}

func TestAcceptAfterTheTreeChangedIsRefused(t *testing.T) {
	isolateUserConfig(t)
	repo, _ := reviewedRepo(t)
	if code, _, stderr := runCLI(t, "override", "--root", repo, "--propose", "--reason", "wrong"); code != 0 {
		t.Fatalf("propose: %s", stderr)
	}
	if err := os.WriteFile(filepath.Join(repo, "later.txt"), []byte("changed after the proposal\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "override", "--root", repo, "--accept")
	if code == 0 || !strings.Contains(stderr, "changed since, so nothing was accepted") {
		t.Fatalf("accept on a changed tree: exit = %d, stderr = %q", code, stderr)
	}
	tree := runGit(t, repo, "rev-parse", "HEAD^{tree}")
	if ok, _ := review.ReviewedWithoutStatic(repo, tree); ok {
		t.Fatal("a stale proposal approved the tree")
	}
}

func TestProposeRefusesWhatAnOverrideCannotAccept(t *testing.T) {
	isolateUserConfig(t)
	repo, _ := reviewedRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "later.txt"), []byte("not reviewed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "override", "--root", repo, "--propose", "--reason", "wrong")
	if code == 0 || !strings.Contains(stderr, "working tree differs from reviewed tree") {
		t.Fatalf("propose on an unreviewed tree: exit = %d, stderr = %q", code, stderr)
	}
	if code, stdout, _ := runCLI(t, "override", "--root", repo, "--pending"); code != 0 || !strings.Contains(stdout, "no proposed override") {
		t.Fatalf("pending: exit = %d, stdout = %q", code, stdout)
	}
}

func TestAcceptAfterTheStaticHalfChangedIsRefused(t *testing.T) {
	isolateUserConfig(t)
	repo, _ := reviewedRepo(t)
	if code, _, stderr := runCLI(t, "override", "--root", repo, "--propose", "--reason", "wrong"); code != 0 {
		t.Fatalf("propose: %s", stderr)
	}
	// A later check of the same tree reuses the review, and its static half
	// now blocks only on a failed analyzer run, which an override could accept.
	store, err := review.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.Last()
	if err != nil {
		t.Fatal(err)
	}
	exit := 1
	last.Mode, last.StaticExit, last.StaticBlocksPush, last.StaticRunFailed, last.StaticStatus = "both", &exit, true, true, "failed"
	err = store.Save(*last)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "override", "--root", repo, "--accept")
	if code == 0 || !strings.Contains(stderr, "changed since, so nothing was accepted") {
		t.Fatalf("accept after the static half changed: exit = %d, stderr = %q", code, stderr)
	}
}
