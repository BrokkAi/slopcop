package prcomment

import (
	"context"
	"fmt"
	"strings"
)

// Target is the pull request to comment on.
type Target struct {
	// Repo is owner/name.
	Repo string
	// PR is the pull request number.
	PR int
	// Commit is the head commit the check ran on, which the inline comments
	// are attached to.
	Commit string
}

// Result is what Publish did.
type Result struct {
	Outcome Outcome
	// Summary is the summary comment's text, without its marker, for the
	// job summary.
	Summary string
	// SummaryURL is the summary comment's link, when one was written.
	SummaryURL string
	// Posted counts the inline comments this run added.
	Posted int
	// Inline counts the findings with an inline comment, this run's or an
	// earlier run's.
	Inline int
	// Elsewhere counts the findings the summary lists because no line of
	// the diff could hold them.
	Elsewhere int
	// ReadOnly is set when the token could not comment, as on a pull
	// request from a fork. Nothing was written to the pull request.
	ReadOnly bool
}

// Render works out the outcome and the summary without GitHub, for a run with
// no pull request, such as a push: every finding is listed in the summary.
func Render(r Report, commit string) Result {
	all := items(r)
	o := outcome(r, all)
	return Result{Outcome: o, Summary: summary(r, o, all, 0, commit, false), Elsewhere: len(all)}
}

// Publish comments the report on the pull request: one review with an inline
// comment for each finding on a line the diff shows that no earlier run
// commented, and the summary comment, created or updated in place. The
// summary comment is created only when the check found something or did not
// pass; a clean check updates one an earlier run left, so it does not go on
// saying there are findings.
func Publish(ctx context.Context, g *GitHub, r Report, t Target) (Result, error) {
	all := items(r)
	o := outcome(r, all)
	result := Result{Outcome: o}

	files, err := g.files(ctx, t.Repo, t.PR)
	if err != nil {
		return result, fmt.Errorf("read the pull request's files: %w", err)
	}
	shown := map[string]diffLines{}
	for _, f := range files {
		shown[f.Filename] = parsePatch(f.Patch)
	}
	me := g.viewer(ctx)
	existing, err := g.reviewComments(ctx, t.Repo, t.PR)
	if err != nil {
		return result, fmt.Errorf("read the pull request's review comments: %w", err)
	}
	commented := map[string]bool{}
	for _, c := range existing {
		// A bot's comment counts too, such as one the SlopCop GitHub App
		// wrote before this run fell back to the token: a person cannot
		// write as a bot, so its markers are never a person's quote.
		if ours(c, me) || c.User.Type == "Bot" {
			for _, key := range markedKeys(c.Body) {
				commented[key] = true
				// The App marks a review finding review:<id>, where this
				// tool marks it <id>. Its static markers use the finding's
				// stable key, which the report does not carry yet.
				if id, ok := strings.CutPrefix(key, "review:"); ok && strings.HasPrefix(id, "rv-") {
					commented[id] = true
				}
			}
		}
	}

	var fresh []reviewComment
	var freshItems, elsewhere []item
	for _, it := range all {
		lines, inDiff := shown[it.path]
		if !inDiff {
			elsewhere = append(elsewhere, it)
			continue
		}
		from, to, ok := lines.anchor(it.start, it.end)
		if !ok {
			elsewhere = append(elsewhere, it)
			continue
		}
		if commented[safeMarker(it.key)] {
			result.Inline++
			continue
		}
		if len(fresh) == maxReviewComments {
			elsewhere = append(elsewhere, it)
			continue
		}
		rc := reviewComment{Path: it.path, Line: to, Side: "RIGHT", Body: it.inlineBody()}
		if from < to {
			rc.StartLine, rc.StartSide = from, "RIGHT"
		}
		fresh = append(fresh, rc)
		freshItems = append(freshItems, it)
	}
	if len(fresh) > 0 {
		body := fmt.Sprintf("%s. The summary comment has the rest.", o.Headline)
		err := g.createReview(ctx, t.Repo, t.PR, newReview{CommitID: t.Commit, Body: quote(body), Event: "COMMENT", Comments: fresh})
		switch {
		case err == nil:
			result.Posted = len(fresh)
			result.Inline += len(fresh)
		case Forbidden(err):
			result.ReadOnly = true
		case unprocessable(err):
			// A line GitHub would not take, such as one a newer push moved:
			// list them all rather than lose them.
			elsewhere = append(elsewhere, freshItems...)
		default:
			return result, fmt.Errorf("post the review comments: %w", err)
		}
	}
	result.Elsewhere = len(elsewhere)
	result.Summary = summary(r, o, elsewhere, result.Inline, t.Commit, false)
	if result.ReadOnly {
		// The read-only token cannot write the summary either, so the job
		// summary lists every finding.
		result.Summary = summary(r, o, all, 0, t.Commit, false)
		result.Elsewhere = len(all)
		return result, nil
	}

	body := summary(r, o, elsewhere, result.Inline, t.Commit, true)
	issueComments, err := g.issueComments(ctx, t.Repo, t.PR)
	if err != nil {
		return result, fmt.Errorf("read the pull request's comments: %w", err)
	}
	var previous *comment
	for i := range issueComments {
		c := issueComments[i]
		if ours(c, me) && strings.Contains(c.Body, summaryMarker) {
			previous = &issueComments[i]
		}
	}
	quiet := r.Status == "clean" && len(all) == 0
	var written comment
	switch {
	case previous != nil:
		written, err = g.updateComment(ctx, t.Repo, previous.ID, body)
		if Forbidden(err) {
			// Another app's summary, such as the SlopCop GitHub App's, which
			// this token may not edit: write one of its own.
			written, err = g.createComment(ctx, t.Repo, t.PR, body)
		}
	case quiet:
		return result, nil
	default:
		written, err = g.createComment(ctx, t.Repo, t.PR, body)
	}
	if Forbidden(err) {
		result.ReadOnly = true
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("write the summary comment: %w", err)
	}
	result.SummaryURL = written.HTMLURL
	return result, nil
}

// ours reports whether a comment is one this tool could have written: by the
// token's own login when it has one, otherwise by a bot, as an Actions or App
// token's comments are. It keeps a person's comment that quotes a marker
// from being edited or read as a finding already answered.
func ours(c comment, me string) bool {
	if me != "" {
		return strings.EqualFold(c.User.Login, me)
	}
	return c.User.Type == "Bot"
}

// markedKeys reads the finding keys out of a comment's markers.
func markedKeys(body string) []string {
	var keys []string
	for {
		at := strings.Index(body, findingMarkerPrefix)
		if at < 0 {
			return keys
		}
		body = body[at+len(findingMarkerPrefix):]
		end := strings.Index(body, " -->")
		if end < 0 {
			return keys
		}
		keys = append(keys, body[:end])
		body = body[end:]
	}
}
