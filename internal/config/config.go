package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const MaxLocalContainers = 6

type Config struct {
	// Server
	ListenAddr string

	// Auth
	APIKey          string
	AnthropicAPIKey string
	OpenAIAPIKey    string

	// Capacity
	ContainersPerInst int

	// Container resources
	ContainerCPUs  int
	ContainerMemGB int

	// Agent
	AgentImage            string
	ReaderImage           string
	DefaultHarness        string
	DefaultClaudeModel    string
	DefaultCodexModel     string
	DefaultEffort         string
	DefaultMaxBudget      float64
	DefaultMaxRuntime     time.Duration
	DefaultMaxTurns       int
	DefaultReadMaxBudget  float64
	DefaultReadMaxRuntime time.Duration
	DefaultReadMaxTurns   int

	// Internal API used by reader containers to query local readings.
	InternalAPIBaseURL string

	// Boolean defaults
	DefaultCreatePR   bool
	DefaultSelfReview bool
	DefaultSaveOutput bool

	// GitHub
	GitHubToken string

	// Webhooks
	WebhookURL    string
	WebhookEvents []string

	// Filesystem data directory (agent output log + task metadata written here)
	DataDir string

	// Logging
	LogFile string

	// Database
	DatabasePath string

	// Retry
	MaxUserRetries int

	// Orchestrator
	PollInterval time.Duration
}

// MaxConcurrent returns the maximum number of concurrent agent containers.
func (c *Config) MaxConcurrent() int {
	return c.ContainersPerInst
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:            envOr("BACKFLOW_LISTEN_ADDR", ":8080"),
		APIKey:                os.Getenv("BACKFLOW_API_KEY"),
		AnthropicAPIKey:       os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:          os.Getenv("OPENAI_API_KEY"),
		ContainersPerInst:     envInt("BACKFLOW_CONTAINERS_PER_INSTANCE", 1),
		ContainerCPUs:         envInt("BACKFLOW_CONTAINER_CPUS", 2),
		ContainerMemGB:        envInt("BACKFLOW_CONTAINER_MEMORY_GB", 8),
		AgentImage:            envOr("BACKFLOW_AGENT_IMAGE", "backflow-agent"),
		ReaderImage:           os.Getenv("BACKFLOW_READER_IMAGE"),
		DefaultHarness:        envOr("BACKFLOW_DEFAULT_HARNESS", "claude_code"),
		DefaultClaudeModel:    envOr("BACKFLOW_DEFAULT_CLAUDE_MODEL", "claude-opus-4-7"),
		DefaultCodexModel:     envOr("BACKFLOW_DEFAULT_CODEX_MODEL", "gpt-5.4"),
		DefaultEffort:         envOr("BACKFLOW_DEFAULT_EFFORT", "xhigh"),
		DefaultMaxBudget:      envFloat("BACKFLOW_DEFAULT_MAX_BUDGET", 10.0),
		DefaultMaxRuntime:     time.Duration(envInt("BACKFLOW_DEFAULT_MAX_RUNTIME_SEC", 1800)) * time.Second,
		DefaultMaxTurns:       envInt("BACKFLOW_DEFAULT_MAX_TURNS", 200),
		DefaultReadMaxBudget:  envFloat("BACKFLOW_DEFAULT_READ_MAX_BUDGET", 0),
		DefaultReadMaxRuntime: time.Duration(envInt("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", 0)) * time.Second,
		DefaultReadMaxTurns:   envInt("BACKFLOW_DEFAULT_READ_MAX_TURNS", 0),
		InternalAPIBaseURL:    os.Getenv("BACKFLOW_INTERNAL_API_BASE_URL"),
		DataDir:               envOr("BACKFLOW_DATA_DIR", "./data"),
		GitHubToken:           os.Getenv("GITHUB_TOKEN"),
		WebhookURL:            os.Getenv("BACKFLOW_WEBHOOK_URL"),
		LogFile:               os.Getenv("BACKFLOW_LOG_FILE"),
		DatabasePath:          envOr("BACKFLOW_DATABASE_PATH", "./backflow.db"),
		MaxUserRetries:        envInt("BACKFLOW_MAX_USER_RETRIES", 2),
		PollInterval:          time.Duration(envInt("BACKFLOW_POLL_INTERVAL_SEC", 5)) * time.Second,
	}

	c.DefaultCreatePR = envBool("BACKFLOW_DEFAULT_CREATE_PR", true)
	c.DefaultSelfReview = envBool("BACKFLOW_DEFAULT_SELF_REVIEW", false)
	c.DefaultSaveOutput = envBool("BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT", true)

	c.WebhookEvents = envCSV("BACKFLOW_WEBHOOK_EVENTS")

	if c.AnthropicAPIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}

	if c.ContainersPerInst > MaxLocalContainers {
		return nil, fmt.Errorf("BACKFLOW_CONTAINERS_PER_INSTANCE must be <= %d, got %d", MaxLocalContainers, c.ContainersPerInst)
	}

	if c.DatabasePath == "" {
		return nil, fmt.Errorf("BACKFLOW_DATABASE_PATH is required")
	}

	if c.ContainerCPUs < 1 {
		return nil, fmt.Errorf("BACKFLOW_CONTAINER_CPUS must be >= 1, got %d", c.ContainerCPUs)
	}
	if c.ContainerMemGB < 1 {
		return nil, fmt.Errorf("BACKFLOW_CONTAINER_MEMORY_GB must be >= 1, got %d", c.ContainerMemGB)
	}

	if c.ReaderImage != "" {
		switch {
		case c.DefaultReadMaxBudget <= 0:
			return nil, fmt.Errorf("BACKFLOW_DEFAULT_READ_MAX_BUDGET must be > 0 when BACKFLOW_READER_IMAGE is set")
		case c.DefaultReadMaxRuntime <= 0:
			return nil, fmt.Errorf("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC must be > 0 when BACKFLOW_READER_IMAGE is set")
		case c.DefaultReadMaxTurns <= 0:
			return nil, fmt.Errorf("BACKFLOW_DEFAULT_READ_MAX_TURNS must be > 0 when BACKFLOW_READER_IMAGE is set")
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

// envCSV returns a trimmed list of values or nil when the variable is unset.
// Callers rely on nil to mean "use all events" for optional filters.
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
