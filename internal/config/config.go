package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Mode string

const (
	ModeEC2     Mode = "ec2"
	ModeLocal   Mode = "local"
	ModeFargate Mode = "fargate"

	MaxLocalContainers = 6
)

type AuthMode string

const (
	AuthModeAPIKey          AuthMode = "api_key"
	AuthModeMaxSubscription AuthMode = "max_subscription"
)

type Config struct {
	// Mode
	Mode Mode

	// Server
	ListenAddr string

	// Auth
	AuthMode              AuthMode
	AnthropicAPIKey       string
	OpenAIAPIKey          string
	ClaudeCredentialsPath string

	// AWS
	AWSRegion         string
	InstanceType      string
	AMI               string
	MaxInstances      int
	ContainersPerInst int
	LaunchTemplateID  string
	ECSCluster        string
	ECSTaskDefinition string
	ECSSubnets        []string
	ECSSecurityGroups []string
	ECSLaunchType     string
	ECSContainerName  string
	ECSAssignPublicIP bool

	// Container resources
	ContainerCPUs  int
	ContainerMemGB int

	// CloudWatch Logs
	CloudWatchLogGroup string
	ECSLogStreamPrefix string

	// Task concurrency
	MaxConcurrentTasks int

	// Agent defaults
	DefaultHarness    string
	DefaultModel      string
	DefaultCodexModel string
	DefaultEffort     string
	DefaultMaxBudget  float64
	DefaultMaxRuntime time.Duration
	DefaultMaxTurns   int

	// GitHub
	GitHubToken string

	// Webhooks
	WebhookURL    string
	WebhookEvents []string

	// S3 (task data: agent output, offloaded config for large prompts)
	S3Bucket string

	// Database
	DBPath string

	// Orchestrator
	PollInterval time.Duration
}

func (c *Config) MaxConcurrent() int {
	if c.Mode == ModeFargate {
		return c.MaxConcurrentTasks
	}
	if c.AuthMode == AuthModeMaxSubscription {
		return 1
	}
	if c.Mode == ModeLocal {
		return c.ContainersPerInst
	}
	return c.MaxInstances * c.ContainersPerInst
}

func Load() (*Config, error) {
	c := &Config{
		Mode:                  Mode(envOr("BACKFLOW_MODE", string(ModeEC2))),
		ListenAddr:            envOr("BACKFLOW_LISTEN_ADDR", ":8080"),
		AuthMode:              AuthMode(envOr("BACKFLOW_AUTH_MODE", string(AuthModeAPIKey))),
		AnthropicAPIKey:       os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:          os.Getenv("OPENAI_API_KEY"),
		ClaudeCredentialsPath: envOr("CLAUDE_CREDENTIALS_PATH", ""),
		AWSRegion:             envOr("AWS_REGION", "us-east-1"),
		InstanceType:          envOr("BACKFLOW_INSTANCE_TYPE", "m7g.xlarge"),
		AMI:                   os.Getenv("BACKFLOW_AMI"),
		MaxInstances:          envInt("BACKFLOW_MAX_INSTANCES", 5),
		ContainersPerInst:     envInt("BACKFLOW_CONTAINERS_PER_INSTANCE", 1),
		ECSCluster:            os.Getenv("BACKFLOW_ECS_CLUSTER"),
		ECSTaskDefinition:     os.Getenv("BACKFLOW_ECS_TASK_DEFINITION"),
		ECSSubnets:            envCSV("BACKFLOW_ECS_SUBNETS"),
		ECSSecurityGroups:     envCSV("BACKFLOW_ECS_SECURITY_GROUPS"),
		ECSLaunchType:         strings.ToUpper(envOr("BACKFLOW_ECS_LAUNCH_TYPE", "FARGATE_SPOT")),
		ECSContainerName:      envOr("BACKFLOW_ECS_CONTAINER_NAME", "backflow-agent"),
		ECSAssignPublicIP:     envBool("BACKFLOW_ECS_ASSIGN_PUBLIC_IP", true),
		ContainerCPUs:         envInt("BACKFLOW_CONTAINER_CPUS", 2),
		ContainerMemGB:        envInt("BACKFLOW_CONTAINER_MEMORY_GB", 8),
		LaunchTemplateID:      os.Getenv("BACKFLOW_LAUNCH_TEMPLATE_ID"),
		CloudWatchLogGroup:    os.Getenv("BACKFLOW_CLOUDWATCH_LOG_GROUP"),
		ECSLogStreamPrefix:    envOr("BACKFLOW_ECS_LOG_STREAM_PREFIX", "ecs"),
		MaxConcurrentTasks:    envInt("BACKFLOW_MAX_CONCURRENT_TASKS", 5),
		DefaultHarness:        envOr("BACKFLOW_DEFAULT_HARNESS", "claude_code"),
		DefaultModel:          envOr("BACKFLOW_DEFAULT_MODEL", "claude-sonnet-4-6"),
		DefaultCodexModel:     envOr("BACKFLOW_DEFAULT_CODEX_MODEL", "gpt-5.4"),
		DefaultEffort:         envOr("BACKFLOW_DEFAULT_EFFORT", "high"),
		DefaultMaxBudget:      envFloat("BACKFLOW_DEFAULT_MAX_BUDGET", 10.0),
		DefaultMaxRuntime:     time.Duration(envInt("BACKFLOW_DEFAULT_MAX_RUNTIME_MIN", 30)) * time.Minute,
		DefaultMaxTurns:       envInt("BACKFLOW_DEFAULT_MAX_TURNS", 200),
		S3Bucket:              os.Getenv("BACKFLOW_S3_BUCKET"),
		GitHubToken:           os.Getenv("GITHUB_TOKEN"),
		WebhookURL:            os.Getenv("BACKFLOW_WEBHOOK_URL"),
		DBPath:                envOr("BACKFLOW_DB_PATH", "backflow.db"),
		PollInterval:          time.Duration(envInt("BACKFLOW_POLL_INTERVAL_SEC", 5)) * time.Second,
	}

	if events := os.Getenv("BACKFLOW_WEBHOOK_EVENTS"); events != "" {
		c.WebhookEvents = strings.Split(events, ",")
		for i := range c.WebhookEvents {
			c.WebhookEvents[i] = strings.TrimSpace(c.WebhookEvents[i])
		}
	}

	if c.Mode != ModeEC2 && c.Mode != ModeLocal && c.Mode != ModeFargate {
		return nil, fmt.Errorf("invalid BACKFLOW_MODE: %q (must be %q, %q, or %q)", c.Mode, ModeEC2, ModeLocal, ModeFargate)
	}

	if c.AuthMode != AuthModeAPIKey && c.AuthMode != AuthModeMaxSubscription {
		return nil, fmt.Errorf("invalid BACKFLOW_AUTH_MODE: %q (must be %q or %q)", c.AuthMode, AuthModeAPIKey, AuthModeMaxSubscription)
	}

	if c.AuthMode == AuthModeAPIKey && c.AnthropicAPIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required when BACKFLOW_AUTH_MODE=api_key")
	}

	if c.Mode == ModeLocal && c.ContainersPerInst > MaxLocalContainers {
		return nil, fmt.Errorf("BACKFLOW_CONTAINERS_PER_INSTANCE must be <= %d in local mode, got %d", MaxLocalContainers, c.ContainersPerInst)
	}

	if c.ContainerCPUs < 1 {
		return nil, fmt.Errorf("BACKFLOW_CONTAINER_CPUS must be >= 1, got %d", c.ContainerCPUs)
	}
	if c.ContainerMemGB < 1 {
		return nil, fmt.Errorf("BACKFLOW_CONTAINER_MEMORY_GB must be >= 1, got %d", c.ContainerMemGB)
	}

	if c.Mode == ModeFargate {
		if c.ECSLaunchType != "FARGATE" && c.ECSLaunchType != "FARGATE_SPOT" {
			return nil, fmt.Errorf("invalid BACKFLOW_ECS_LAUNCH_TYPE: %q (must be %q or %q)", c.ECSLaunchType, "FARGATE", "FARGATE_SPOT")
		}
		if c.MaxConcurrentTasks < 1 {
			return nil, fmt.Errorf("BACKFLOW_MAX_CONCURRENT_TASKS must be >= 1, got %d", c.MaxConcurrentTasks)
		}
		switch {
		case c.AuthMode == AuthModeMaxSubscription:
			return nil, fmt.Errorf("BACKFLOW_AUTH_MODE=%s is not supported in %s mode", AuthModeMaxSubscription, ModeFargate)
		case c.ECSCluster == "":
			return nil, fmt.Errorf("BACKFLOW_ECS_CLUSTER is required when BACKFLOW_MODE=%s", ModeFargate)
		case c.ECSTaskDefinition == "":
			return nil, fmt.Errorf("BACKFLOW_ECS_TASK_DEFINITION is required when BACKFLOW_MODE=%s", ModeFargate)
		case len(c.ECSSubnets) == 0:
			return nil, fmt.Errorf("BACKFLOW_ECS_SUBNETS is required when BACKFLOW_MODE=%s", ModeFargate)
		case c.CloudWatchLogGroup == "":
			return nil, fmt.Errorf("BACKFLOW_CLOUDWATCH_LOG_GROUP is required when BACKFLOW_MODE=%s", ModeFargate)
		}
	}

	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(os.Getenv(key))
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}

func envCSV(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}

	parts := strings.Split(v, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}
