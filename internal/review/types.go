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
	// Sonnet is Claude Sonnet through the Claude ACP adapter, restricted as
	// Haiku is.
	Sonnet Family = "sonnet"
	// Opus is Claude Opus through the Claude ACP adapter, restricted as
	// Haiku is.
	Opus Family = "opus"
	// Gemini is Google Gemini through the Antigravity CLI's ACP server. It is
	// opt-in: Antigravity's web search cannot be turned off or refused, so it
	// runs only when named in review.reviewers or review.fallbacks.
	Gemini Family = "gemini"
	// Kimi is Moonshot's Kimi K3 through Kimi Code CLI's ACP server. It is
	// opt-in: it needs an API key, and it sends the change to Moonshot.
	Kimi Family = "kimi"
	// Grok is xAI Grok through the Grok Build CLI's ACP server. It is opt-in:
	// its restrictions have layers SlopCop cannot verify from outside, such
	// as what the CLI itself sends over the network.
	Grok Family = "grok"
	// Ollama is a model the user installed in Ollama, through OpenCode's ACP
	// server. It is opt-in: it needs a chosen model, and a small local model
	// is a weaker reviewer, so it runs only when named in review.reviewers or
	// review.fallbacks.
	Ollama Family = "ollama"
	// OpenRouter is a model the user names in review.models.openrouter, run
	// by SlopCop's read-only chat agent. It is opt-in: it needs that choice
	// and sends the review through a third party.
	OpenRouter Family = "openrouter"
)

// Families lists, in priority order, the families `slopcop init` may choose.
// Sonnet and Opus follow Haiku, so a machine with Claude Code alone gets a
// second Claude reviewer instead of one, and Opus, the costliest, comes last.
// Gemini, Kimi, Grok, Ollama and OpenRouter are left out because they are opt-in.
var Families = []Family{Terra, Sol, Flash, Haiku, Sonnet, Opus}

const (
	MaxReviewers         = 4
	DefaultReviewerCount = 2
	// DefaultTimeout bounds each reviewer, from adapter launch to its reply.
	DefaultTimeout = 20 * time.Minute
	// DefaultMinCompleted is how many reviewers must finish for a review
	// to be clean when review.minCompleted is unset, or the number of
	// reviewers when fewer are configured.
	DefaultMinCompleted = 2
	// CleanReply is the exact reply that approves the change.
	CleanReply = "No findings."
)

// Status is the outcome of a review, or of one reviewer.
type Status string

const (
	// StatusClean: at least Settings.MinCompleted reviewers replied exactly
	// CleanReply, and every other reviewer failed after its retry.
	StatusClean Status = "clean"
	// StatusFindings: at least one reviewer reported findings. Other
	// reviewers may have failed.
	StatusFindings Status = "findings"
	// StatusFailed: fewer than Settings.MinCompleted reviewers finished
	// and none reported findings, or the review could not run.
	StatusFailed Status = "failed"
	// StatusEmpty: the base tree and the working-tree snapshot are the same,
	// so there was nothing to review and no reviewer ran.
	StatusEmpty Status = "empty"
)

// Settings choose and bound the reviewers.
type Settings struct {
	Reviewers       []Family
	ReviewerEfforts []string
	// Timeout bounds each reviewer.
	Timeout time.Duration
	// Fallbacks stand in, in order, for reviewers that still fail after
	// their retry, while fewer than MinCompleted reviewers have finished.
	Fallbacks       []Family
	FallbackEfforts []string
	// MinCompleted is how many reviewers, primaries or fallbacks, must
	// finish for a clean review; a reviewer that still fails is reported but
	// does not block one. Zero means DefaultMinCompleted, capped at the
	// number of reviewers.
	MinCompleted int
	// Adapters overrides argv by adapter name. An adapter without an entry
	// uses its pinned npx package.
	Adapters map[string][]string
	// Provider, when ProviderCopilot, runs every family GitHub Copilot
	// serves through the Copilot CLI instead of the family's own adapter.
	Provider string
	// Models names the model of each family that does not choose its own:
	// an installed Ollama model for Ollama, a vendor/model for a chat family.
	Models map[Family]string
	// DSHKeyFile, when set, is an absolute path to a file holding the
	// DeepSeek API key. The default dsh command passes it with
	// --api-key-file; a configured dsh command is used as written.
	DSHKeyFile string
}

// ProviderCopilot routes the families Copilot serves through Copilot CLI.
const ProviderCopilot = "copilot"

func (s Settings) minCompleted() int {
	if s.MinCompleted > 0 {
		return s.MinCompleted
	}
	return min(DefaultMinCompleted, max(len(s.Reviewers), 1))
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
	// Branch is the branch that was checked out when the snapshot was taken,
	// "" when HEAD was detached. The review's rounds are that branch's. It is
	// not stored: the round ledger already keys the rounds by it.
	Branch       string   `json:"branch,omitempty"`
	ChangedLines int      `json:"changedLines,omitempty"`
	MergedIn     []string `json:"mergedIn,omitempty"`
	Unattributed string   `json:"unattributed,omitempty"`
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
	// Adapter names the ACP adapter that ran the reviewer.
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Status  Status `json:"status"`
	// Text is the reviewer's final reply: agent message text after its last
	// tool call.
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
	// Log is the reviewer's inbound ACP stream as JSON lines.
	Log string `json:"log,omitempty"`
	// FirstError and FirstLog describe the failed first attempt of a
	// reviewer that was retried; the fields above describe the retry.
	FirstError string `json:"firstError,omitempty"`
	FirstLog   string `json:"firstLog,omitempty"`
	// StandsInFor names the reviewer a fallback replaced.
	StandsInFor string `json:"standsInFor,omitempty"`
	// Warning says how the reviewer ran differently from what was asked,
	// such as on Copilot's Auto model or at its default effort.
	Warning string `json:"warning,omitempty"`
	// UnreadChange says which changed file a reviewer that reported findings
	// could not read. Its findings stand, but overriding them cannot approve
	// the change, since the reviewer did not cover it.
	UnreadChange string `json:"unreadChange,omitempty"`
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
	// Incomplete is set when fewer than Settings.MinCompleted reviewers
	// finished; with findings, the check still cannot pass.
	Incomplete bool `json:"incomplete,omitempty"`
	// Reused is set when the verdict is an earlier complete review of the
	// same capture by the same reviewers, returned without running again:
	// with the same intent, or, for a clean review of a tree still approved
	// for push, with any intent.
	Reused bool      `json:"reused,omitempty"`
	Time   time.Time `json:"time"`
	// Overridden are the findings that matched a finding the author overrode
	// with `slopcop override <rv-id>`, or that repeat, at lines unchanged
	// since, a finding of an earlier round the author accepted with
	// `slopcop override --reason`. Findings leaves them out, and they do not
	// make the review's Status findings.
	Overridden []OverriddenFinding `json:"overridden,omitempty"`
	// PromptOverrides are the ids of the overridden findings the reviewers
	// were told not to report. A review is not reused once one is undone: a
	// reviewer may have left that finding out only because it was told to.
	PromptOverrides []string `json:"promptOverrides,omitempty"`
	// DurationsMs is how long each reviewer ran, retry included, by Name. It
	// is kept here rather than on Reviewer because the check's JSON report
	// prints Reviewer as it is.
	DurationsMs map[string]int64 `json:"durationsMs,omitempty"`
}

// OverriddenFinding is a reported finding that matched a finding override.
type OverriddenFinding struct {
	Finding
	// OverrideID is the id of the overridden finding it matched: its own ID when
	// it is the same text, and another when a reviewer reworded it.
	OverrideID string `json:"overrideId"`
	// Match says how it matched: MatchText or MatchWords.
	Match string `json:"match"`
	// Round, when set, is the earlier review round whose override accepted
	// the finding it repeats, at lines unchanged since; OverrideID is then
	// that finding's id. Zero means a finding override matched.
	Round int `json:"round,omitempty"`
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
	// ContentKey is ReuseKey without the intent: what the reviewers read
	// and who read it. A clean review of an approved tree is reused when
	// only the intent changed.
	ContentKey string `json:"contentKey,omitempty"`
	// Mode is "both" or "ai".
	Mode string `json:"mode"`
	// StaticExit is the static half's exit code; nil in "ai" mode.
	StaticExit *int `json:"staticExit,omitempty"`
	// StaticBlocksPush records whether the hosted result prevents approval.
	StaticBlocksPush bool `json:"staticBlocksPush,omitempty"`
	// StaticRunFailed records that the hosted result blocks only because an
	// analyzer run failed and checked nothing: it reported no finding at or
	// above the push level, and the check judged the review to pass or its
	// findings overridable. `slopcop override --reason` may accept such a
	// tree. A record from before it leaves it false, so it never does.
	StaticRunFailed bool `json:"staticRunFailed,omitempty"`
	// StaticStatus is the hosted half's own status; empty in AI mode.
	StaticStatus string `json:"staticStatus,omitempty"`
	// Accepted is the `slopcop override` that accepted this review's findings.
	// It is on the record rather than the verdict so that a later check, which
	// saves a new record even when it reuses the verdict, does not inherit it.
	Accepted *AcceptedOverride `json:"acceptedOverride,omitempty"`
	// Check is the hosted check this review ran beside, when it answered with
	// an id. Like Accepted it is on the record, so a later check that reuses
	// the verdict does not inherit it.
	Check *CheckRef `json:"check,omitempty"`
	Verdict
}

// AcceptedOverride is an override of a whole review, as it was made. Whether
// it still applies is OverrideState's to say.
type AcceptedOverride struct {
	Reason string    `json:"reason"`
	Time   time.Time `json:"time"`
}
