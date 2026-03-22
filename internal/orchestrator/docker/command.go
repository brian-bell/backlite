package docker

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
func (m *Manager) runCommand(ctx context.Context, instanceID, command string) (string, error) {
	if m.config.Mode == config.ModeLocal {
		return m.runLocalCommand(ctx, command)
	}
	return m.runSSMCommand(ctx, instanceID, command)
}

// runLocalCommand executes a bash command on the local machine.
func (m *Manager) runLocalCommand(ctx context.Context, command string) (string, error) {
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
func (m *Manager) runSSMCommand(ctx context.Context, instanceID, command string) (string, error) {
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
func (m *Manager) ssmDiagnosticError(ctx context.Context, input *ssm.GetCommandInvocationInput, waitErr error) error {
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
func (m *Manager) ensureClient(ctx context.Context) error {
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
