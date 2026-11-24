package review

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A workspace can turn on review tracking. The check answer then says so, and
// the CLI reports what its reviewers found, so the workspace sees it beside
// the static findings. This file builds that report and remembers which
// checks were tracked, for the overrides made later. It never sends the
// prompt, the diff, the intent text or a reviewer's transcript, and what a
// reviewer's reply yields is only ever read from the parsed findings.

// TrackingFindings is the check answer's reviewTracking value that turns
// reporting on. Every other value, absent included, means send nothing.
const TrackingFindings = "findings"

// The service's bounds on a report.
const (
	maxReportReviewers = 8
	maxReportFindings  = 100
	// maxReportBytes leaves room under the service's 256 KiB.
	maxReportBytes  = 250 * 1024
	maxTitleChars   = 200
	maxMessageChars = 500
	maxAPIChars     = 100
	// MaxDispositionReason is the longest override reason the service keeps.
	MaxDispositionReason = 500
	// inlineCodeLimit is the longest inline code span a sent message keeps.
	inlineCodeLimit = 40
	codeOmitted     = "[code omitted]"
)

// Parse quality of a reviewer's reply.
const (
	ParseParsed   = "parsed"
	ParsePartial  = "partial"
	ParseUnparsed = "unparsed"
)

// Report is the POST /v1/check/{checkId}/review body. Everything but a
// finding's own fields is what the CLI knows about its run, never read from a
// reply.
type Report struct {
	CLIVersion   string `json:"cliVersion"`
	Mode         string `json:"mode"`
	IntentSource string `json:"intentSource,omitempty"`
	BaseTree     string `json:"baseTree"`
	Tree         string `json:"tree"`
	Reused       bool   `json:"reused"`
	Status       Status `json:"status"`
	// PromptOverrides are the finding overrides the reviewers were told not
	// to report, so a reply can be clean because of them. The service does
	// not read it yet, and drops it.
	PromptOverrides []string         `json:"promptOverrides,omitempty"`
	Reviewers       []ReportReviewer `json:"reviewers"`
}

// ReportReviewer is one reviewer the CLI ran, a fallback included.
type ReportReviewer struct {
	Position       int    `json:"position"`
	Family         Family `json:"family"`
	Adapter        string `json:"adapter,omitempty"`
	AdapterVersion string `json:"adapterVersion,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Status         Status `json:"status"`
	DurationMs     *int64 `json:"durationMs,omitempty"`
	ParseQuality   string `json:"parseQuality,omitempty"`
	// FindingCount is how many findings the CLI read in the reply, those cut
	// from Findings and those a standing override left out included.
	FindingCount int             `json:"findingCount"`
	Findings     []ReportFinding `json:"findings"`
	// OverriddenCount and Overridden say which of the reply's findings a
	// standing finding override left out of this review. The service does not
	// read them yet, and drops them.
	OverriddenCount int                `json:"overriddenCount,omitempty"`
	Overridden      []ReportOverridden `json:"overridden,omitempty"`
}

// ReportFinding is one finding read from a reply. Its text goes only with the
// source range it cites.
type ReportFinding struct {
	Key       string `json:"key"`
	Priority  string `json:"priority,omitempty"`
	Path      string `json:"path,omitempty"`
	StartLine *int   `json:"startLine,omitempty"`
	EndLine   *int   `json:"endLine,omitempty"`
	Title     string `json:"title,omitempty"`
	Message   string `json:"message,omitempty"`
	Category  string `json:"category,omitempty"`
	API       string `json:"api,omitempty"`
}

// Keys are the ids of the findings the report carries.
func (r Report) Keys() []string {
	keys := []string{}
	for _, row := range r.Reviewers {
		for _, f := range row.Findings {
			if !slices.Contains(keys, f.Key) {
				keys = append(keys, f.Key)
			}
		}
	}
	return keys
}

// ReportOverridden is a finding a standing override left out of a review.
type ReportOverridden struct {
	Key         string `json:"key"`
	OverrideKey string `json:"overrideKey"`
	Match       string `json:"match"`
}

// BuildReport makes the report of a review, cut to the service's bounds.
// command names the argv each family ran with. ok is false when no reviewer
// ran, since the first report of a check stands and this one would say
// nothing.
func BuildReport(v Verdict, version string, command func(Family) []string) (report Report, ok bool) {
	if len(v.Reviewers) == 0 {
		return Report{}, false
	}
	report = Report{
		CLIVersion:   version,
		Mode:         "both",
		IntentSource: intentLabel(v.IntentSource),
		BaseTree:     v.Capture.BaseTree,
		// The tree the reviewers read, without any omitted large files.
		Tree:   v.Capture.CheckedTree(),
		Reused: v.Reused,
		Status: v.Status,
	}
	report.PromptOverrides = v.PromptOverrides[:min(len(v.PromptOverrides), maxReportFindings)]
	changed := ChangedPaths(v.Capture.Numstat)
	left := make(map[string]OverriddenFinding, len(v.Overridden))
	for _, f := range v.Overridden {
		left[f.instance()] = f
	}
	for i, r := range v.Reviewers[:min(len(v.Reviewers), maxReportReviewers)] {
		row := ReportReviewer{
			Position: i + 1, Family: r.Family, Adapter: r.Adapter, Model: r.Model,
			Effort: r.Effort, Status: r.Status, Findings: []ReportFinding{},
		}
		if row.Status == "" {
			row.Status = parseVerdict(r)
		}
		if command != nil {
			row.AdapterVersion = adapterVersion(command(r.Family))
		}
		if d, found := v.DurationsMs[r.Name]; found {
			row.DurationMs = &d
		}
		switch {
		case row.Status == StatusFailed && r.Adapter == ollamaAdapter && verdictFormatError(r.Text) != nil:
			// The reviewer replied, but not in the format, so the CLI failed it.
			// It is counted as one unparsed finding; its text is not sent.
			row.ParseQuality = ParseUnparsed
			row.FindingCount = 1
		case row.Status == StatusClean:
			row.ParseQuality = ParseParsed
		case row.Status == StatusFindings:
			findings := ParseFindings(r.Name, r.Text, changed)
			row.ParseQuality = parseQuality(findings)
			row.FindingCount = len(findings)
			for _, f := range findings {
				if o, overridden := left[f.instance()]; overridden {
					row.OverriddenCount++
					if len(row.Overridden) < maxReportFindings {
						row.Overridden = append(row.Overridden, ReportOverridden{Key: f.ID, OverrideKey: o.OverrideID, Match: o.Match})
					}
				} else if len(row.Findings) < maxReportFindings {
					row.Findings = append(row.Findings, reportFinding(f))
				}
			}
		}
		report.Reviewers = append(report.Reviewers, row)
	}
	report.fit(maxReportBytes)
	return report, true
}

// fit drops the last findings of the longest lists until the report's JSON
// is within limit. The counts keep what the reviewers really reported.
func (r *Report) fit(limit int) {
	for {
		body, err := json.Marshal(r)
		if err != nil || len(body) <= limit {
			return
		}
		longest := -1
		for i, row := range r.Reviewers {
			if len(row.Findings) > 0 && (longest < 0 || len(row.Findings) > len(r.Reviewers[longest].Findings)) {
				longest = i
			}
		}
		if longest >= 0 {
			row := &r.Reviewers[longest]
			row.Findings = row.Findings[:len(row.Findings)-max(len(row.Findings)/8, 1)]
			continue
		}
		for i, row := range r.Reviewers {
			if len(row.Overridden) > 0 && (longest < 0 || len(row.Overridden) > len(r.Reviewers[longest].Overridden)) {
				longest = i
			}
		}
		if longest < 0 {
			return
		}
		row := &r.Reviewers[longest]
		row.Overridden = row.Overridden[:len(row.Overridden)-max(len(row.Overridden)/8, 1)]
	}
}

// parseQuality says how much of a reply followed the format: a reply with no
// tagged finding is unparsed, one with a finding that names no place partial.
func parseQuality(findings []Finding) string {
	quality := ParseParsed
	for _, f := range findings {
		if f.Malformed {
			return ParseUnparsed
		}
		if f.Priority == "" || f.Path == "" || f.StartLine == nil {
			quality = ParsePartial
		}
	}
	return quality
}

// reportFinding is a finding as sent. Without a safe relative path and a line
// range it is a counted row with no text, as the service would store it, so
// reviewer prose that cites nothing does not leave the machine.
func reportFinding(f Finding) ReportFinding {
	out := ReportFinding{Key: f.ID, Priority: f.Priority, Category: f.Category}
	if !safeReportPath(f.Path) || f.StartLine == nil || f.EndLine == nil || *f.StartLine < 1 || *f.EndLine < *f.StartLine {
		return out
	}
	out.Path, out.StartLine, out.EndLine = f.Path, f.StartLine, f.EndLine
	out.Title = reportText(f.Title, maxTitleChars)
	out.Message = reportText(f.Message, maxMessageChars)
	// An API name gets the same code filter as the rest of the text, and is
	// left out when that removed anything: what is left is not a name.
	if api := reportText(strings.Trim(f.API, "`"), maxAPIChars); utf8.RuneCountInString(f.API) <= maxAPIChars && !strings.Contains(api, codeOmitted) && !strings.Contains(api, "`") {
		out.API = api
	}
	return out
}

var (
	// fencedCode is a code block, closed or running to the end of the text.
	fencedCode = regexp.MustCompile("(?s)```.*?(?:```|$)|~~~.*?(?:~~~|$)")
	// inlineCode is a code span in single or double backticks; a double one
	// may hold a single backtick.
	inlineCode = regexp.MustCompile("(?s)``.+?``|`[^`]*`")
)

// reportText is reviewer prose as sent: on one line, without fenced code or a
// long inline code span, which can quote source or a secret, and cut at max
// characters.
func reportText(text string, limit int) string {
	text = fencedCode.ReplaceAllString(text, codeOmitted)
	text = inlineCode.ReplaceAllStringFunc(text, func(span string) string {
		// Code inside backticks does not start or end with a space. Text that
		// does lies between two spans, or after a lone backtick, and is prose.
		content := strings.Trim(span, "`")
		if strings.TrimSpace(content) != content {
			return span
		}
		if utf8.RuneCountInString(content) > inlineCodeLimit {
			return codeOmitted
		}
		return span
	})
	return cutRunes(oneLine(text), limit)
}

// DispositionReason is an override's reason as sent: one line, within the
// service's bound.
func DispositionReason(reason string) string {
	return cutRunes(oneLine(reason), MaxDispositionReason)
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

func cutRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:limit]))
}

// safeReportPath accepts a repository-relative path with no way out of the
// repository, so an absolute path that names the user's home is never sent.
func safeReportPath(path string) bool {
	if path == "" || len(path) > 1024 || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) || filepath.IsAbs(path) {
		return false
	}
	if strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// intentLabel is the source of the review's intent as a short label. The
// verdict's own wording is a sentence for the reviewers.
func intentLabel(source string) string {
	switch {
	case strings.Contains(source, "Claude Code transcript"):
		return "claude-transcript"
	case strings.Contains(source, "Codex rollout"):
		return "codex-rollout"
	case strings.Contains(source, "--intent"):
		return "intent-flag"
	case source == "":
		return ""
	}
	return "none"
}

// pinnedPackage is an npm package pinned to a version, as a default adapter
// command names it.
var pinnedPackage = regexp.MustCompile(`^(?:@[\w.-]+/)?[\w.-]+@(\d[\w.+-]*)$`)

// adapterVersion is the version an adapter's command pins, or "". Nothing
// else of the command is read: a configured one can hold local paths.
func adapterVersion(command []string) string {
	for _, arg := range command {
		if match := pinnedPackage.FindStringSubmatch(arg); match != nil {
			return match[1]
		}
	}
	return ""
}

// CheckRef names a hosted check, and where to reach it again.
type CheckRef struct {
	ID string `json:"id"`
	// Service and Workspace are the service URL and workspace id the check
	// ran against; a later report about it goes to the same ones.
	Service   string `json:"service,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	// Tracked says the check's answer turned review tracking on.
	Tracked bool `json:"tracked,omitempty"`
}

const (
	// trackedChecksFile lists the clone's recent checks once one was tracked.
	trackedChecksFile = "review-tracked-checks.json"
	// maxTrackedChecks bounds that list, and so what one undo is sent to.
	maxTrackedChecks = 50
	// maxKeptChecks bounds it even when standing overrides concern them all.
	maxKeptChecks = 200
	// trackedLockWait bounds the wait for that list after a check's report
	// is printed, which must not hold the check up.
	trackedLockWait = 500 * time.Millisecond
)

// trackedCheck is one check whose review was reported.
type trackedCheck struct {
	CheckRef
	Time time.Time `json:"time"`
	// Worktree is the checkout that ran the review. An override's reason goes
	// only to a check of the checkout it was made in.
	Worktree string `json:"worktree,omitempty"`
	// Keys are the ids of the findings its reviewers reported.
	Keys []string `json:"keys,omitempty"`
	// Relied are the ids of the finding overrides its review relied on.
	Relied []string `json:"relied,omitempty"`
}

type trackedCheckList struct {
	Checks []trackedCheck `json:"checks"`
}

func readTrackedChecks(dir string) ([]trackedCheck, error) {
	b, err := os.ReadFile(filepath.Join(dir, trackedChecksFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list trackedCheckList
	if err = json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list.Checks, nil
}

// RememberCheck records a check for the clone, so an override made later, from
// any worktree, can name it. A check that is not tracked is kept too, once the
// list exists: it is what stops a finding it reported from being sent to an
// older tracked check. replace is false when the service may already hold a
// first report of this check, which stands, so an entry for it is kept.
// keys are the findings the report carried; nil takes the verdict's own.
func RememberCheck(root string, ref CheckRef, v Verdict, keys []string, replace bool) error {
	if ref.ID == "" {
		return nil
	}
	entry := trackedCheck{CheckRef: ref, Time: time.Now().UTC(), Worktree: worktreeKey(root), Relied: v.usedOverrides()}
	// Only the findings the report carried: one a standing override left out,
	// or one cut to fit the service's bounds, belongs to a check that sent it.
	if keys == nil {
		for _, f := range v.Findings() {
			keys = append(keys, f.ID)
		}
	}
	for _, key := range keys {
		if !slices.Contains(entry.Keys, key) {
			entry.Keys = append(entry.Keys, key)
		}
	}
	return withCloneLock(root, trackedChecksFile+".lock", trackedLockWait, func(dir string) error {
		checks, err := readTrackedChecks(dir)
		if err != nil {
			// The list only helps later reports; a damaged one starts over.
			checks = nil
		}
		if !ref.Tracked && len(checks) == 0 {
			return nil
		}
		// The first checkout to report a check keeps it: the service kept that
		// report, and another checkout's findings are not in it.
		known := slices.ContainsFunc(checks, func(c trackedCheck) bool { return c.ID == ref.ID })
		if known && !replace {
			return nil
		}
		checks = slices.DeleteFunc(checks, func(c trackedCheck) bool { return c.ID == ref.ID })
		checks = append(checks, entry)
		standing, _ := readOverrides(dir)
		checks = trimTrackedChecks(checks, standing)
		return (&Store{dir: dir}).write(trackedChecksFile, trackedCheckList{Checks: checks})
	})
}

// RememberLocalReview records a review that ran beside no hosted check, under
// an id of its own that is never sent, so it is never tracked.
func RememberLocalReview(root string, v Verdict) error {
	if len(v.Reviewers) == 0 {
		return nil
	}
	id := "local:" + v.Capture.Tree + ":" + strconv.FormatInt(v.Time.UnixNano(), 10)
	return RememberCheck(root, CheckRef{ID: id}, v, nil, true)
}

// trimTrackedChecks drops the oldest checks past maxTrackedChecks, keeping
// those a standing override concerns, so its undo can still reach them, up to
// maxKeptChecks in all.
func trimTrackedChecks(checks []trackedCheck, standing []FindingOverride) []trackedCheck {
	concerned := func(c trackedCheck) bool {
		return slices.ContainsFunc(standing, func(o FindingOverride) bool {
			return slices.Contains(c.Keys, o.ID) || slices.Contains(c.Relied, o.ID)
		})
	}
	excess := len(checks) - maxTrackedChecks
	kept := make([]trackedCheck, 0, len(checks))
	for _, c := range checks {
		if excess > 0 && !concerned(c) {
			excess--
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) > maxKeptChecks {
		kept = kept[len(kept)-maxKeptChecks:]
	}
	return kept
}

// TrackedCheckOf is the check an override of the finding is about: the latest
// review's when it reported the finding, else the newest check of this
// worktree on the list that did. ok is false when that check is not tracked, so an override never
// reaches a workspace through an older check.
func TrackedCheckOf(root, key string) (CheckRef, bool) {
	if last, err := LastRecord(root); err == nil && last != nil &&
		slices.ContainsFunc(last.reported(), func(f Finding) bool { return f.ID == key }) {
		if last.Check == nil {
			return CheckRef{}, false
		}
		// A check on the list holds the findings of its first report, which
		// is the one the service kept.
		checks := loadTrackedChecks(root)
		here := worktreeKey(root)
		i := slices.IndexFunc(checks, func(c trackedCheck) bool { return c.ID == last.Check.ID && c.Worktree == here })
		switch {
		case i < 0, !checks[i].Tracked:
			return CheckRef{}, false
		case slices.Contains(checks[i].Keys, key):
			return checks[i].CheckRef, true
		}
		// The latest check did not record it: a standing override left it out
		// there, or the service kept an earlier report. An earlier check of
		// this worktree that recorded it is the one to tell.
	}
	checks := loadTrackedChecks(root)
	for i := len(checks) - 1; i >= 0; i-- {
		if checks[i].Worktree == worktreeKey(root) && slices.Contains(checks[i].Keys, key) {
			return checks[i].CheckRef, checks[i].Tracked
		}
	}
	return CheckRef{}, false
}

// CurrentCheck is ref as the list knows it for this worktree, with the
// workspace its report named and whether the service kept that report. A
// check reported only from another checkout, or never, is not tracked here.
func CurrentCheck(root string, ref CheckRef) CheckRef {
	here := worktreeKey(root)
	for _, c := range loadTrackedChecks(root) {
		if c.ID == ref.ID && c.Worktree == here {
			return c.CheckRef
		}
	}
	ref.Tracked = false
	return ref
}

// worktreeKey names a checkout the same way however its path was reached.
func worktreeKey(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

// KnownCheck reports whether this worktree's report of the check is on the
// list.
func KnownCheck(root, id string) bool {
	here := worktreeKey(root)
	return slices.ContainsFunc(loadTrackedChecks(root), func(c trackedCheck) bool { return c.ID == id && c.Worktree == here })
}

// TrackedChecksRelyingOn are the tracked checks of this worktree an undo of the finding
// override concerns, newest first: those that reported the finding and those
// whose review relied on the override.
func TrackedChecksRelyingOn(root, id string) []CheckRef {
	checks := loadTrackedChecks(root)
	var refs []CheckRef
	for i := len(checks) - 1; i >= 0; i-- {
		if checks[i].Tracked && checks[i].Worktree == worktreeKey(root) && (slices.Contains(checks[i].Keys, id) || slices.Contains(checks[i].Relied, id)) {
			refs = append(refs, checks[i].CheckRef)
		}
	}
	return refs
}

func loadTrackedChecks(root string) []trackedCheck {
	dir, err := overridesDir(root)
	if err != nil {
		return nil
	}
	checks, err := readTrackedChecks(dir)
	if err != nil {
		return nil
	}
	return checks
}
