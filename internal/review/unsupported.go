package review

import (
	"context"
	"fmt"

	acp "github.com/coder/acp-go-sdk"
)

// These capabilities are not advertised. Reviewers must use their own tools.
type unsupportedClient struct{}

var unsupported = fmt.Errorf("client filesystem and terminal capabilities are not supported; use agent-owned tools")

// ReadTextFile refuses client-side tools because agents use their own tools.
func (unsupportedClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, unsupported
}

// WriteTextFile refuses client-side tools because agents use their own tools.
func (unsupportedClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, unsupported
}

// CreateTerminal refuses client-side tools because agents use their own tools.
func (unsupportedClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, unsupported
}

// TerminalOutput refuses client-side tools because agents use their own tools.
func (unsupportedClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, unsupported
}

// ReleaseTerminal refuses client-side tools because agents use their own tools.
func (unsupportedClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, unsupported
}

// WaitForTerminalExit refuses client-side tools because agents use their own tools.
func (unsupportedClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, unsupported
}

// KillTerminal refuses client-side tools because agents use their own tools.
func (unsupportedClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, unsupported
}
