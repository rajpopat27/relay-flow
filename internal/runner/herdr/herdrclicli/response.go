package herdrclicli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type responseEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *apiError       `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "Herdr API request failed"
}

// runJSON executes a read/create operation, decodes its response envelope,
// and unmarshals result into dest. It intentionally reports only the
// operation name in command failures; command arguments can contain prompts
// and are not suitable for diagnostic output.
func (c *CLI) runJSON(ctx context.Context, operation string, dest any, args ...string) error {
	stdout, stderr, err := c.execute(ctx, args...)
	if err != nil {
		return commandError(operation, stdout, stderr, err)
	}
	return decodeResponse(operation, stdout, dest)
}

// runCommand executes a mutating operation. Herdr normally returns no body
// for these commands, but a JSON API error is still honored if one is
// returned with a successful process exit.
func (c *CLI) runCommand(ctx context.Context, operation string, args ...string) error {
	stdout, stderr, err := c.execute(ctx, args...)
	if err != nil {
		return commandError(operation, stdout, stderr, err)
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil
	}
	if responseErr := responseError(stdout); responseErr != nil {
		return fmt.Errorf("herdr %s: %w", operation, responseErr)
	}
	return nil
}

func decodeResponse(operation string, output []byte, dest any) error {
	var envelope responseEnvelope
	if err := json.Unmarshal(output, &envelope); err != nil {
		return fmt.Errorf("herdr %s: malformed JSON response: %w", operation, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("herdr %s: %w", operation, envelope.Error)
	}
	if !envelope.OK {
		return fmt.Errorf("herdr %s: unsuccessful API response", operation)
	}
	if dest == nil {
		return nil
	}
	if len(bytes.TrimSpace(envelope.Result)) == 0 {
		return fmt.Errorf("herdr %s: response missing result", operation)
	}
	if err := json.Unmarshal(envelope.Result, dest); err != nil {
		return fmt.Errorf("herdr %s: malformed result: %w", operation, err)
	}
	return nil
}

func responseError(output []byte) error {
	var envelope responseEnvelope
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if !envelope.OK {
		return fmt.Errorf("unsuccessful API response")
	}
	return nil
}

func commandError(operation string, stdout, stderr []byte, commandErr error) error {
	if responseErr := responseError(stdout); responseErr != nil {
		return fmt.Errorf("herdr %s: %w", operation, responseErr)
	}
	message := strings.TrimSpace(string(stderr))
	if message == "" {
		return fmt.Errorf("herdr %s: command failed: %w", operation, commandErr)
	}
	return fmt.Errorf("herdr %s: command failed: %w: %s", operation, commandErr, message)
}
