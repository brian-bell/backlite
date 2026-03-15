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
	ModeEC2   Mode = "ec2"
	ModeLocal Mode = "local"

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
	ClaudeCredentialsPath string

	// AWS
	AWSRegion         string
	InstanceType      string
	AMI               string
	MaxInstances      int
	ContainersPerInst int
	LaunchTemplateID  string

	// Container resources
	ContainerCPUs  int
	ContainerMemGB int

	// Agent defaults
	DefaultModel      string
	DefaultEffort     string
	DefaultMaxBudget  float64
	DefaultMaxRuntime time.Duration
	DefaultMaxTurns   int

	// GitHub
	GitHubToken string

	// Webhooks
	WebhookURL    string
	WebhookEvents []string

	// Database
	DBPath string

	// Orchestrator
	PollInterval time.Duration
}

func (c *Config) MaxConcurrent() int {
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
		ClaudeCredentialsPath: envOr("CLAUDE_CREDENTIALS_PATH", ""),
		AWSRegion:             envOr("AWS_REGION", "us-east-1"),
		InstanceType:          envOr("BACKFLOW_INSTANCE_TYPE", "m7g.xlarge"),
		AMI:                   os.Getenv("BACKFLOW_AMI"),
		MaxInstances:          envInt("BACKFLOW_MAX_INSTANCES", 5),
		ContainersPerInst:     envInt("BACKFLOW_CONTAINERS_PER_INSTANCE", 1),
		ContainerCPUs:         envInt("BACKFLOW_CONTAINER_CPUS", 2),
		ContainerMemGB:        envInt("BACKFLOW_CONTAINER_MEMORY_GB", 8),
		LaunchTemplateID:      os.Getenv("BACKFLOW_LAUNCH_TEMPLATE_ID"),
		DefaultModel:          envOr("BACKFLOW_DEFAULT_MODEL", "claude-sonnet-4-6"),
		DefaultEffort:         envOr("BACKFLOW_DEFAULT_EFFORT", "high"),
		DefaultMaxBudget:      envFloat("BACKFLOW_DEFAULT_MAX_BUDGET", 10.0),
		DefaultMaxRuntime:     time.Duration(envInt("BACKFLOW_DEFAULT_MAX_RUNTIME_MIN", 30)) * time.Minute,
		DefaultMaxTurns:       envInt("BACKFLOW_DEFAULT_MAX_TURNS", 200),
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

	if c.Mode != ModeEC2 && c.Mode != ModeLocal {
		return nil, fmt.Errorf("invalid BACKFLOW_MODE: %q (must be %q or %q)", c.Mode, ModeEC2, ModeLocal)
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
