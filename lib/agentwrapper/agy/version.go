package agy

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Version returns the version of the installed agy CLI.
func Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "agy", "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running agy --version: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}
