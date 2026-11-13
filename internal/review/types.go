// Package review runs the local LLM review half of a check: independent
// reviewer agents, started over the Agent Client Protocol, read the change
// from the base tree to a snapshot of the working tree, and each must answer
// exactly "No findings." for the review to be clean.
package review

import (
	"io"
	"time"
)

// Family is a reviewer model family, in the order reviewers are chosen.
type Family string

const (
	// Terra is GPT Terra through the Codex ACP adapter.
	Terra Family = "terra"
	// Sol is GPT Sol through the Codex ACP adapter.
	Sol Family = "sol"
	// Flash is DeepSeek Flash through the dsh ACP adapter.
	Flash Family = "flash"
	// Haiku is Claude Haiku through the Claude ACP adapter.
	Haiku Family = "haiku"
)

// Families lists every family in priority order.
var Families = []Family{Terra, Sol, Flash, Haiku}

const (
	MaxReviewers         = 4
	DefaultReviewerCount = 2
	// DefaultTimeout bounds each reviewer, from adapter launch to its reply.
	DefaultTimeout = 20 * time.Minute
	// CleanReply is the exact reply that approves the change.
	CleanReply = "No findings."
)

// Status is the outcome of a review, or of one reviewer.
type Status string

const (
	// StatusClean: every reviewer replied exactly CleanReply.
	StatusClean Status = "clean"
	// StatusFindings: at least one reviewer reported findings and none
	// failed.
	StatusFindings Status = "findings"
	// StatusFailed: a reviewer failed, timed out, or replied empty.
	// Partial findings may still be present in Reviewers.
	StatusFailed Status = "failed"
	// StatusEmpty: the base tree and the working-tree snapshot are the same,
	// so there was nothing to review and no reviewer ran.
	StatusEmpty Status = "empty"
)

// Settings choose and bound the reviewers.
type Settings struct {
	Reviewers []Family
	// Timeout bounds each reviewer.
	Timeout time.Duration
	// Adapters overrides argv by adapter name. An adapter without an entry
	// uses its pinned npx package.
	Adapters map[string][]string
}

// Capture names exactly what was reviewed.
type Capture struct {
	BaseCommit string `json:"baseCommit"`
	BaseTree   string `json:"baseTree"`
	// Tree is the snapshot of the working tree: HEAD plus every tracked,
	// untracked and deleted path, without ignored files.
	Tree string `json:"tree"`
	// DiffTree is the subset reviewed after large data artifacts are omitted.
	// Tree remains the full snapshot identity used for approval and overrides.
	DiffTree string `json:"diffTree,omitempty"`
	// Numstat is `git diff --numstat BaseTree CheckedTree()`.
	Numstat string `json:"numstat"`
}

// CheckedTree names the contents actually sent to the reviewers.
func (c Capture) CheckedTree() string {
	if c.DiffTree != "" {
		return c.DiffTree
	}
	return c.Tree
}

// Empty reports whether the capture has nothing to review.
func (c Capture) Empty() bool { return c.BaseTree == c.CheckedTree() }

// Reviewer is one reviewer's result.
type Reviewer struct {
	// Name is "<position>-<family>", such as "1-terra".
	Name   string `json:"name"`
	Family Family `json:"family"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Status Status `json:"status"`
	// Text is the reviewer's final reply: agent message text after its last
	// tool call.
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
	// Log is the reviewer's inbound ACP stream as JSON lines.
	Log string `json:"log,omitempty"`
}

// Verdict is the outcome of one review.
type Verdict struct {
	Status       Status     `json:"status"`
	Capture      Capture    `json:"capture"`
	IntentSource string     `json:"intentSource,omitempty"`
	LogDir       string     `json:"logDir,omitempty"`
	Reviewers    []Reviewer `json:"reviewers,omitempty"`
	// Error says why a failed review failed when no single reviewer is
	// responsible, such as missing configuration or unwritable state.
	Error string `json:"error,omitempty"`
	// Reused is set when the verdict is an earlier complete review of the
	// same capture by the same reviewers, returned without running again.
	Reused bool      `json:"reused,omitempty"`
	Time   time.Time `json:"time"`
}

// Options are the inputs of Run.
type Options struct {
	// Mode is the check's mode, "both" or "ai", recorded in last.json.
	Mode string
	// Version is the slopcop version; it is part of the reuse key, since
	// another release may prompt differently.
	Version string
	// Root is the repository's top-level directory.
	Root string
	// BaseCommit is the commit the change is reviewed against; the check's
	// static half uses the same one.
	BaseCommit string
	// Intent is the --intent fallback, used only when no transcript names
	// the user's own messages.
	Intent   string
	Settings Settings
	// Progress, when set, receives one-line progress notes for a person.
	Progress io.Writer
}

// Record is what last.json holds: the latest check that ran a review, with
// the static half's exit and status when the check had one, so an override
// can reject static findings, failures, and pending checks.
type Record struct {
	ReuseKey string `json:"reuseKey,omitempty"`
	// Mode is "both" or "ai".
	Mode string `json:"mode"`
	// StaticExit is the static half's exit code; nil in "ai" mode.
	StaticExit *int `json:"staticExit,omitempty"`
	// StaticBlocksPush records whether the hosted result prevents approval.
	StaticBlocksPush bool `json:"staticBlocksPush,omitempty"`
	// StaticStatus is the hosted half's own status; empty in AI mode.
	StaticStatus string `json:"staticStatus,omitempty"`
	Verdict
}
