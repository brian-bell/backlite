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

type Config struct {
	// Mode
	Mode Mode

	// Server
	ListenAddr string

	// Auth
	APIKey          string
	AnthropicAPIKey string
	OpenAIAPIKey    string

	// AWS
	AWSRegion               string
	InstanceType            string
	AMI                     string
	MaxInstances            int
	ContainersPerInst       int
	LaunchTemplateID        string
	ECSCluster              string
	ECSTaskDefinition       string
	ECSReaderTaskDefinition string
	ECSSubnets              []string
	ECSSecurityGroups       []string
	ECSLaunchType           string
	ECSContainerName        string
	ECSAssignPublicIP       bool

	// Container resources
	ContainerCPUs  int
	ContainerMemGB int

	// CloudWatch Logs
	CloudWatchLogGroup string
	ECSLogStreamPrefix string

	// Task concurrency
	MaxConcurrentTasks int

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

	// Supabase (passed through to reader containers)
	SupabaseURL     string
	SupabaseAnonKey string

	// Boolean defaults
	DefaultCreatePR   bool
	DefaultSelfReview bool
	DefaultSaveOutput bool

	// GitHub
	GitHubToken string

	// Webhooks
	WebhookURL    string
	WebhookEvents []string

	// SMS / Messaging
	SMSProvider        string
	TwilioAccountSID   string
	TwilioAuthToken    string
	SMSFromNumber      string
	SMSEvents          []string
	SMSOutboundEnabled bool

	// Discord
	DiscordAppID        string
	DiscordPublicKey    string
	DiscordBotToken     string
	DiscordGuildID      string
	DiscordChannelID    string
	DiscordCommandName  string
	DiscordAllowedRoles []string
	DiscordEvents       []string

	// S3 (task data: agent output, offloaded config for large prompts)
	S3Bucket string

	// Logging
	LogFile string

	// Database
	DatabaseURL string

	// Retry
	MaxUserRetries int

	// Orchestrator
	PollInterval time.Duration
}

func (c *Config) DiscordEnabled() bool { return c.DiscordAppID != "" }

func (c *Config) MaxConcurrent() int {
	if c.Mode == ModeFargate {
		return c.MaxConcurrentTasks
	}
	if c.Mode == ModeLocal {
		return c.ContainersPerInst
	}
	return c.MaxInstances * c.ContainersPerInst
}

func Load() (*Config, error) {
	c := &Config{
		Mode:                    Mode(envOr("BACKFLOW_MODE", string(ModeEC2))),
		ListenAddr:              envOr("BACKFLOW_LISTEN_ADDR", ":8080"),
		APIKey:                  os.Getenv("BACKFLOW_API_KEY"),
		AnthropicAPIKey:         os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:            os.Getenv("OPENAI_API_KEY"),
		AWSRegion:               envOr("AWS_REGION", "us-east-1"),
		InstanceType:            envOr("BACKFLOW_INSTANCE_TYPE", "m7g.xlarge"),
		AMI:                     os.Getenv("BACKFLOW_AMI"),
		MaxInstances:            envInt("BACKFLOW_MAX_INSTANCES", 5),
		ContainersPerInst:       envInt("BACKFLOW_CONTAINERS_PER_INSTANCE", 1),
		ECSCluster:              os.Getenv("BACKFLOW_ECS_CLUSTER"),
		ECSTaskDefinition:       os.Getenv("BACKFLOW_ECS_TASK_DEFINITION"),
		ECSReaderTaskDefinition: os.Getenv("BACKFLOW_ECS_READER_TASK_DEFINITION"),
		ECSSubnets:              envCSV("BACKFLOW_ECS_SUBNETS"),
		ECSSecurityGroups:       envCSV("BACKFLOW_ECS_SECURITY_GROUPS"),
		ECSLaunchType:           strings.ToUpper(envOr("BACKFLOW_ECS_LAUNCH_TYPE", "FARGATE_SPOT")),
		ECSContainerName:        envOr("BACKFLOW_ECS_CONTAINER_NAME", "backflow-agent"),
		ECSAssignPublicIP:       envBool("BACKFLOW_ECS_ASSIGN_PUBLIC_IP", true),
		ContainerCPUs:           envInt("BACKFLOW_CONTAINER_CPUS", 2),
		ContainerMemGB:          envInt("BACKFLOW_CONTAINER_MEMORY_GB", 8),
		LaunchTemplateID:        os.Getenv("BACKFLOW_LAUNCH_TEMPLATE_ID"),
		CloudWatchLogGroup:      os.Getenv("BACKFLOW_CLOUDWATCH_LOG_GROUP"),
		ECSLogStreamPrefix:      envOr("BACKFLOW_ECS_LOG_STREAM_PREFIX", "ecs"),
		MaxConcurrentTasks:      envInt("BACKFLOW_MAX_CONCURRENT_TASKS", 5),
		AgentImage:              envOr("BACKFLOW_AGENT_IMAGE", "backflow-agent"),
		ReaderImage:             os.Getenv("BACKFLOW_READER_IMAGE"),
		DefaultHarness:          envOr("BACKFLOW_DEFAULT_HARNESS", "claude_code"),
		DefaultClaudeModel:      envOr("BACKFLOW_DEFAULT_CLAUDE_MODEL", "claude-sonnet-4-6"),
		DefaultCodexModel:       envOr("BACKFLOW_DEFAULT_CODEX_MODEL", "gpt-5.4"),
		DefaultEffort:           envOr("BACKFLOW_DEFAULT_EFFORT", "medium"),
		DefaultMaxBudget:        envFloat("BACKFLOW_DEFAULT_MAX_BUDGET", 10.0),
		DefaultMaxRuntime:       time.Duration(envInt("BACKFLOW_DEFAULT_MAX_RUNTIME_SEC", 1800)) * time.Second,
		DefaultMaxTurns:         envInt("BACKFLOW_DEFAULT_MAX_TURNS", 200),
		DefaultReadMaxBudget:    envFloat("BACKFLOW_DEFAULT_READ_MAX_BUDGET", 0),
		DefaultReadMaxRuntime:   time.Duration(envInt("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", 0)) * time.Second,
		DefaultReadMaxTurns:     envInt("BACKFLOW_DEFAULT_READ_MAX_TURNS", 0),
		SupabaseURL:             os.Getenv("SUPABASE_URL"),
		SupabaseAnonKey:         os.Getenv("SUPABASE_ANON_KEY"),
		S3Bucket:                os.Getenv("BACKFLOW_S3_BUCKET"),
		GitHubToken:             os.Getenv("GITHUB_TOKEN"),
		WebhookURL:              os.Getenv("BACKFLOW_WEBHOOK_URL"),
		DiscordAppID:            os.Getenv("BACKFLOW_DISCORD_APP_ID"),
		DiscordPublicKey:        os.Getenv("BACKFLOW_DISCORD_PUBLIC_KEY"),
		DiscordBotToken:         os.Getenv("BACKFLOW_DISCORD_BOT_TOKEN"),
		DiscordGuildID:          os.Getenv("BACKFLOW_DISCORD_GUILD_ID"),
		DiscordChannelID:        os.Getenv("BACKFLOW_DISCORD_CHANNEL_ID"),
		DiscordCommandName:      envOr("BACKFLOW_DISCORD_COMMAND_NAME", "backflow"),
		DiscordAllowedRoles:     envCSV("BACKFLOW_DISCORD_ALLOWED_ROLES"),
		DiscordEvents:           envCSV("BACKFLOW_DISCORD_EVENTS"),
		LogFile:                 os.Getenv("BACKFLOW_LOG_FILE"),
		DatabaseURL:             os.Getenv("BACKFLOW_DATABASE_URL"),
		MaxUserRetries:          envInt("BACKFLOW_MAX_USER_RETRIES", 2),
		PollInterval:            time.Duration(envInt("BACKFLOW_POLL_INTERVAL_SEC", 5)) * time.Second,
	}

	c.DefaultCreatePR = envBool("BACKFLOW_DEFAULT_CREATE_PR", true)
	c.DefaultSelfReview = envBool("BACKFLOW_DEFAULT_SELF_REVIEW", false)
	c.DefaultSaveOutput = envBool("BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT", true)

	c.SMSProvider = envOr("BACKFLOW_SMS_PROVIDER", "")
	c.TwilioAccountSID = os.Getenv("TWILIO_ACCOUNT_SID")
	c.TwilioAuthToken = os.Getenv("TWILIO_AUTH_TOKEN")
	c.SMSFromNumber = os.Getenv("BACKFLOW_SMS_FROM_NUMBER")
	c.SMSEvents = envCSVOrDefault("BACKFLOW_SMS_EVENTS", []string{"task.completed", "task.failed"})
	c.SMSOutboundEnabled = envOr("BACKFLOW_SMS_OUTBOUND_ENABLED", "true") == "true"
	c.WebhookEvents = envCSV("BACKFLOW_WEBHOOK_EVENTS")

	if c.Mode != ModeEC2 && c.Mode != ModeLocal && c.Mode != ModeFargate {
		return nil, fmt.Errorf("invalid BACKFLOW_MODE: %q (must be %q, %q, or %q)", c.Mode, ModeEC2, ModeLocal, ModeFargate)
	}

	if c.AnthropicAPIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}

	if c.Mode == ModeLocal && c.ContainersPerInst > MaxLocalContainers {
		return nil, fmt.Errorf("BACKFLOW_CONTAINERS_PER_INSTANCE must be <= %d in local mode, got %d", MaxLocalContainers, c.ContainersPerInst)
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("BACKFLOW_DATABASE_URL is required")
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

	if c.SMSProvider != "" {
		switch c.SMSProvider {
		case "twilio":
			if c.TwilioAccountSID == "" || c.TwilioAuthToken == "" || c.SMSFromNumber == "" {
				return nil, fmt.Errorf("TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, and BACKFLOW_SMS_FROM_NUMBER are required when BACKFLOW_SMS_PROVIDER=twilio")
			}
		default:
			return nil, fmt.Errorf("invalid BACKFLOW_SMS_PROVIDER: %q (must be %q)", c.SMSProvider, "twilio")
		}
	}

	if c.DiscordAppID != "" {
		switch {
		case c.DiscordPublicKey == "":
			return nil, fmt.Errorf("BACKFLOW_DISCORD_PUBLIC_KEY is required when BACKFLOW_DISCORD_APP_ID is set")
		case c.DiscordBotToken == "":
			return nil, fmt.Errorf("BACKFLOW_DISCORD_BOT_TOKEN is required when BACKFLOW_DISCORD_APP_ID is set")
		case c.DiscordGuildID == "":
			return nil, fmt.Errorf("BACKFLOW_DISCORD_GUILD_ID is required when BACKFLOW_DISCORD_APP_ID is set")
		case c.DiscordChannelID == "":
			return nil, fmt.Errorf("BACKFLOW_DISCORD_CHANNEL_ID is required when BACKFLOW_DISCORD_APP_ID is set")
		}
	}

	if c.ReaderImage != "" {
		switch {
		case c.DefaultReadMaxBudget <= 0:
			return nil, fmt.Errorf("BACKFLOW_DEFAULT_READ_MAX_BUDGET must be > 0 when BACKFLOW_READER_IMAGE is set")
		case c.DefaultReadMaxRuntime <= 0:
			return nil, fmt.Errorf("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC must be > 0 when BACKFLOW_READER_IMAGE is set")
		case c.DefaultReadMaxTurns <= 0:
			return nil, fmt.Errorf("BACKFLOW_DEFAULT_READ_MAX_TURNS must be > 0 when BACKFLOW_READER_IMAGE is set")
		case c.SupabaseURL == "":
			return nil, fmt.Errorf("SUPABASE_URL is required when BACKFLOW_READER_IMAGE is set")
		case c.SupabaseAnonKey == "":
			return nil, fmt.Errorf("SUPABASE_ANON_KEY is required when BACKFLOW_READER_IMAGE is set")
		case c.Mode == ModeFargate && c.ECSReaderTaskDefinition == "":
			return nil, fmt.Errorf("BACKFLOW_ECS_READER_TASK_DEFINITION is required when BACKFLOW_READER_IMAGE is set in %s mode", ModeFargate)
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

func envCSVOrDefault(key string, fallback []string) []string {
	values := envCSV(key)
	if values == nil {
		return fallback
	}
	return values
}
