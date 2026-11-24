package prcomment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// perPage is the page size of every listing; GitHub's maximum.
	perPage = 100
	// maxPages bounds a listing. A pull request's files API stops at 3,000
	// files, and a pull request with more than 3,000 comments is past
	// helping.
	maxPages = 30
	// maxReviewComments bounds the inline comments one review carries; the
	// rest go in the summary.
	maxReviewComments = 50
)

// GitHub is a small client for the pull request endpoints the comments need.
type GitHub struct {
	// BaseURL is the REST API root, such as https://api.github.com, or a
	// GitHub Enterprise Server's https://host/api/v3.
	BaseURL string
	Token   string
	// UserAgent names the client to GitHub, which requires one.
	UserAgent string
	HTTP      *http.Client
}

// APIError is a response GitHub refused.
type APIError struct {
	Status  int
	Message string
	Path    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub answered %d for %s: %s", e.Status, e.Path, e.Message)
}

// Forbidden reports whether the token may not do what was asked, as on a
// pull request from a fork, whose token is read-only.
func Forbidden(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Status == http.StatusForbidden || api.Status == http.StatusUnauthorized)
}

// unprocessable reports whether GitHub refused the request's content, as it
// does a review comment on a line outside the diff.
func unprocessable(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusUnprocessableEntity
}

func (g *GitHub) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.UserAgent != "" {
		req.Header.Set("User-Agent", g.UserAgent)
	}
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Message == "" {
			problem.Message = http.StatusText(resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Message: problem.Message, Path: path}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GitHub %s %s: read the answer: %w", method, path, err)
	}
	return nil
}

// list reads every page of a listing into out, a pointer to a slice.
func list[T any](ctx context.Context, g *GitHub, path string) ([]T, error) {
	var all []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= maxPages; page++ {
		var batch []T
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s%sper_page=%d&page=%d", path, sep, perPage, page), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// prFile is one file of a pull request's diff.
type prFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Patch    string `json:"patch"`
}

// user is the author of a comment.
type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// comment is an issue comment or a pull request review comment.
type comment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    user   `json:"user"`
}

// reviewComment is one inline comment of a new review.
type reviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

type newReview struct {
	CommitID string          `json:"commit_id,omitempty"`
	Body     string          `json:"body"`
	Event    string          `json:"event"`
	Comments []reviewComment `json:"comments"`
}

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q is not owner/name", repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func (g *GitHub) files(ctx context.Context, repo string, pr int) ([]prFile, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[prFile](ctx, g, fmt.Sprintf("%s/pulls/%d/files", base, pr))
}

func (g *GitHub) reviewComments(ctx context.Context, repo string, pr int) ([]comment, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[comment](ctx, g, fmt.Sprintf("%s/pulls/%d/comments", base, pr))
}

func (g *GitHub) issueComments(ctx context.Context, repo string, pr int) ([]comment, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[comment](ctx, g, fmt.Sprintf("%s/issues/%d/comments", base, pr))
}

func (g *GitHub) createReview(ctx context.Context, repo string, pr int, review newReview) error {
	base, err := repoPath(repo)
	if err != nil {
		return err
	}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/reviews", base, pr), review, nil)
}

func (g *GitHub) createComment(ctx context.Context, repo string, pr int, body string) (comment, error) {
	base, err := repoPath(repo)
	if err != nil {
		return comment{}, err
	}
	var created comment
	err = g.do(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", base, pr), map[string]string{"body": body}, &created)
	return created, err
}

func (g *GitHub) updateComment(ctx context.Context, repo string, id int64, body string) (comment, error) {
	base, err := repoPath(repo)
	if err != nil {
		return comment{}, err
	}
	var updated comment
	err = g.do(ctx, http.MethodPatch, fmt.Sprintf("%s/issues/comments/%d", base, id), map[string]string{"body": body}, &updated)
	return updated, err
}

// viewer is the login the token acts as, or "" for a GitHub App or Actions
// token, which may not read /user.
func (g *GitHub) viewer(ctx context.Context) string {
	var me user
	if err := g.do(ctx, http.MethodGet, "/user", nil, &me); err != nil {
		return ""
	}
	return me.Login
}
