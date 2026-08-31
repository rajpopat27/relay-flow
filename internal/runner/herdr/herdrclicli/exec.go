package herdrclicli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// execute invokes the installed Herdr executable and keeps its two output
// streams separate. Command-specific response parsing and error handling are
// deliberately left to the callers that implement the public operations.
func (c *CLI) execute(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	if err := validateAbsoluteCWD(args); err != nil {
		return nil, nil, err
	}

	cmd := exec.CommandContext(ctx, "herdr", args...)
	cmd.Env = c.environment(os.Environ())

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

func (c *CLI) environment(base []string) []string {
	env := append([]string(nil), base...)
	if c.options.Session != "" {
		env = setEnvironmentValue(env, "HERDR_SESSION", c.options.Session)
	}
	if c.options.SocketPath != "" {
		env = setEnvironmentValue(env, "HERDR_SOCKET_PATH", c.options.SocketPath)
	}
	return env
}

func setEnvironmentValue(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func validateAbsoluteCWD(args []string) error {
	for i, arg := range args {
		if arg != "--cwd" {
			continue
		}
		if i+1 >= len(args) {
			return fmt.Errorf("herdr --cwd requires a path")
		}
		cwd := args[i+1]
		if !filepath.IsAbs(cwd) {
			return fmt.Errorf("herdr --cwd must be absolute: %q", cwd)
		}
	}
	return nil
}
