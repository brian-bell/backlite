package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/backflow-labs/backflow/internal/config"
)

// runCommand routes to either local or SSM execution based on config mode.
func (m *DockerManager) runCommand(ctx context.Context, instanceID, command string) (string, error) {
	if m.config.Mode == config.ModeLocal {
		return m.runLocalCommand(ctx, command)
	}
	return m.runSSMCommand(ctx, instanceID, command)
}

// runLocalCommand executes a bash command on the local machine.
func (m *DockerManager) runLocalCommand(ctx context.Context, command string) (string, error) {
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

// runSSMCommand sends a shell command to an EC2 instance via AWS SSM and waits
// for the result (up to 5 minutes).
func (m *DockerManager) runSSMCommand(ctx context.Context, instanceID, command string) (string, error) {
	if err := m.ensureClient(ctx); err != nil {
		return "", err
	}

	result, err := m.ssmClient.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters:   map[string][]string{"commands": {command}},
	})
	if err != nil {
		return "", fmt.Errorf("ssm send command: %w", err)
	}

	invocationInput := &ssm.GetCommandInvocationInput{
		CommandId:  result.Command.CommandId,
		InstanceId: aws.String(instanceID),
	}

	waiter := ssm.NewCommandExecutedWaiter(m.ssmClient)
	if err := waiter.Wait(ctx, invocationInput, 5*time.Minute); err != nil {
		return "", m.ssmDiagnosticError(ctx, invocationInput, err)
	}

	out, err := m.ssmClient.GetCommandInvocation(ctx, invocationInput)
	if err != nil {
		return "", fmt.Errorf("get command output: %w", err)
	}
	return aws.ToString(out.StandardOutputContent), nil
}

// ssmDiagnosticError fetches stderr/stdout from a failed SSM invocation to
// produce a more useful error message.
func (m *DockerManager) ssmDiagnosticError(ctx context.Context, input *ssm.GetCommandInvocationInput, waitErr error) error {
	out, err := m.ssmClient.GetCommandInvocation(ctx, input)
	if err != nil {
		return fmt.Errorf("wait for command: %w", waitErr)
	}

	status := string(out.Status)
	detail := strings.TrimSpace(aws.ToString(out.StandardErrorContent))
	if detail == "" {
		detail = strings.TrimSpace(aws.ToString(out.StandardOutputContent))
	}
	if detail != "" {
		return fmt.Errorf("ssm command failed (status=%s): %s", status, detail)
	}
	return fmt.Errorf("ssm command failed (status=%s): %w", status, waitErr)
}

// ensureClient lazily initializes the AWS SSM client.
func (m *DockerManager) ensureClient(ctx context.Context) error {
	if m.ssmClient != nil {
		return nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(m.config.AWSRegion))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	m.ssmClient = ssm.NewFromConfig(cfg)
	return nil
}

// shellEscape wraps a string in single quotes, escaping any embedded single
// quotes so it is safe to interpolate into a shell command.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// isInstanceGone returns true if the error indicates the EC2 instance no
// longer exists or is not reachable via SSM (e.g. terminated, shutting down).
func isInstanceGone(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "InvalidInstanceId") ||
		strings.Contains(msg, "InvalidInstanceID")
}

// isHexString returns true if s is a non-empty string of hex characters (used
// to validate Docker container IDs).
func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
