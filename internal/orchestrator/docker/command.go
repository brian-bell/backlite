package docker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runCommand executes a bash command on the local host.
func (m *Manager) runCommand(ctx context.Context, _ /*instanceID*/, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return "", fmt.Errorf("local command failed: %s", detail)
		}
		return "", fmt.Errorf("local command failed: %w", err)
	}
	return stdout.String(), nil
}
