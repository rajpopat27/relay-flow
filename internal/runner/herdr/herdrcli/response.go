package herdrcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Observed Herdr transport contract (herdr 0.8.2, protocol 20):
//
//	success: JSON envelope {"id":...,"result":{...}} on stdout, exit 0.
//	         pane run writes nothing on either stream.
//	failure: JSON envelope {"id":...,"error":{"code","message"}} on stderr,
//	         exit 1, empty stdout.
//
// There is no "ok" field; presence of result/error is the contract.
type responseEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *apiError       `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func sentinelFor(code string) error {
	switch code {
	case "pane_not_found":
		return ErrPaneNotFound
	case "workspace_not_found":
		return ErrWorkspaceNotFound
	case "worktree_not_found":
		return ErrWorktreeNotFound
	case "not_git_worktree":
		return ErrNotGitWorktree
	default:
		return nil
	}
}

// runJSON executes an operation that returns a result body and unmarshals it
// into dest. Command arguments can contain prompts, so failures report only
// the operation name and Herdr's own message.
func (c *CLI) runJSON(ctx context.Context, operation string, dest any, args ...string) error {
	stdout, stderr, err := c.execute(ctx, args...)
	if apiErr := decodeError(operation, stderr); apiErr != nil {
		return apiErr
	}
	if err != nil {
		return commandError(operation, stderr, err)
	}
	return decodeResult(operation, stdout, dest)
}

// runCommand executes a mutating operation. Herdr returns either an "ok"
// result body or no body at all; only the error envelope matters.
func (c *CLI) runCommand(ctx context.Context, operation string, args ...string) error {
	_, stderr, err := c.execute(ctx, args...)
	if apiErr := decodeError(operation, stderr); apiErr != nil {
		return apiErr
	}
	if err != nil {
		return commandError(operation, stderr, err)
	}
	return nil
}

// decodeError converts a Herdr error envelope on stderr into a typed error.
// Non-JSON stderr is not an error by itself; Herdr also uses stderr for
// warnings.
func decodeError(operation string, stderr []byte) error {
	trimmed := bytes.TrimSpace(stderr)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(trimmed, &envelope); err != nil || envelope.Error == nil {
		return nil
	}
	message := envelope.Error.Message
	if message == "" {
		message = envelope.Error.Code
	}
	if sentinel := sentinelFor(envelope.Error.Code); sentinel != nil {
		return fmt.Errorf("herdr %s: %s: %w", operation, message, sentinel)
	}
	return fmt.Errorf("herdr %s: %s (%s)", operation, message, envelope.Error.Code)
}

func decodeResult(operation string, stdout []byte, dest any) error {
	if dest == nil {
		return nil
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return fmt.Errorf("herdr %s: malformed JSON response: %w", operation, err)
	}
	if len(bytes.TrimSpace(envelope.Result)) == 0 {
		return fmt.Errorf("herdr %s: response missing result", operation)
	}
	if err := json.Unmarshal(envelope.Result, dest); err != nil {
		return fmt.Errorf("herdr %s: malformed result: %w", operation, err)
	}
	return nil
}

func commandError(operation string, stderr []byte, commandErr error) error {
	message := strings.TrimSpace(string(stderr))
	if message == "" {
		return fmt.Errorf("herdr %s: command failed: %w", operation, commandErr)
	}
	return fmt.Errorf("herdr %s: command failed: %w: %s", operation, commandErr, message)
}
