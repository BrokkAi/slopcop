package service

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	whoamiPath          = "/v1/whoami"
	workspacePath       = "/v1/workspace"
	repositoriesPath    = "/v1/repositories"
	repositoryStatePath = "/v1/repositories/state"
)

// Account is the person behind a login.
type Account struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

// Workspace is one workspace of the account, with the account's role in it.
// Current marks the one the login is bound to: its checks and ignores run in,
// and bill, that workspace.
type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Role    string `json:"role"`
	Current bool   `json:"current"`
}

// Whoami is the GET /v1/whoami body: the account, the login's workspace and
// its plan ("pro" or "free"), and every workspace of the account.
type Whoami struct {
	Account    Account     `json:"account"`
	Workspace  Workspace   `json:"workspace"`
	Plan       string      `json:"plan"`
	Workspaces []Workspace `json:"workspaces"`
}

// Repository is one repository of the login's workspace. Enabled is the
// owner's choice; Active is whether it may be checked now: enabled and
// within the workspace's active limit.
type Repository struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	FullName string `json:"fullName"`
	Enabled  bool   `json:"enabled"`
	Active   bool   `json:"active"`
}

// RepositoryList is the GET /v1/repositories body.
type RepositoryList struct {
	WorkspaceID string `json:"workspaceId"`
	// ActiveRepositoryLimit is nil when the workspace has no limit.
	ActiveRepositoryLimit *int64       `json:"activeRepositoryLimit"`
	ActiveRepositories    int64        `json:"activeRepositories"`
	CanManageRepositories bool         `json:"canManageRepositories"`
	Repositories          []Repository `json:"repositories"`
}

// RepositoryState is the POST /v1/repositories/state answer.
type RepositoryState struct {
	RepositoryID string `json:"repositoryId"`
	FullName     string `json:"fullName"`
	Enabled      bool   `json:"enabled"`
	Active       bool   `json:"active"`
}

// Whoami reads the login's account, workspace and plan.
func (c *Client) Whoami(ctx context.Context) (Whoami, error) {
	var decoded Whoami
	if err := c.read(ctx, whoamiPath, &decoded); err != nil {
		return Whoami{}, err
	}
	if decoded.Workspaces == nil {
		decoded.Workspaces = []Workspace{}
	}
	return decoded, nil
}

// UseWorkspace binds the login, and only the login, to another workspace of
// the account.
func (c *Client) UseWorkspace(ctx context.Context, workspaceID string) error {
	var decoded struct {
		WorkspaceID string `json:"workspaceId"`
	}
	return c.write(ctx, workspacePath, map[string]string{"workspaceId": workspaceID}, &decoded)
}

// Repositories lists the login's workspace's repositories.
func (c *Client) Repositories(ctx context.Context) (RepositoryList, error) {
	var decoded RepositoryList
	if err := c.read(ctx, repositoriesPath, &decoded); err != nil {
		return RepositoryList{}, err
	}
	if decoded.Repositories == nil {
		decoded.Repositories = []Repository{}
	}
	return decoded, nil
}

// SetRepositoryEnabled enables or disables one repository of the login's
// workspace. Only the workspace's owner may.
func (c *Client) SetRepositoryEnabled(ctx context.Context, repositoryID string, enabled bool) (RepositoryState, error) {
	var decoded RepositoryState
	err := c.write(ctx, repositoryStatePath, struct {
		RepositoryID string `json:"repositoryId"`
		Enabled      bool   `json:"enabled"`
	}{repositoryID, enabled}, &decoded)
	return decoded, err
}

func (c *Client) read(ctx context.Context, path string, into any) error {
	status, _, payload, err := c.getJSON(ctx, path)
	if err != nil {
		return err
	}
	return decodeAnswer(status, path, payload, into)
}

func (c *Client) write(ctx context.Context, path string, body, into any) error {
	status, _, payload, err := c.postJSON(ctx, path, body)
	if err != nil {
		return err
	}
	return decodeAnswer(status, path, payload, into)
}

// decodeAnswer decodes a 2xx body into into, and anything else into *Error.
func decodeAnswer(status int, path string, payload []byte, into any) error {
	if status < 200 || status >= 300 {
		return decodeError(status, payload)
	}
	if err := json.Unmarshal(payload, into); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}
