package connector

import (
	_ "embed"

	up "go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	// Top-level blocks to match example-config.yaml structure
	IMAP       IMAPConfig       `yaml:"imap"`
	Logging    LoggingConfig    `yaml:"logging"`
	Processing ProcessingConfig `yaml:"email_processing"`
	// GmailOAuth holds the user-provided "Desktop app" OAuth 2.0 client
	// credentials used for the device-code login flow on Gmail accounts.
	GmailOAuth GmailOAuthConfig `yaml:"gmail_oauth"`
	// Keep Network for internal use but don't map to YAML
	Network NetworkConfig `yaml:"-"`
}

// GmailOAuthConfig is the on-disk shape of the gmail_oauth: block. The
// runtime variant lives in pkg/email; this type only carries the YAML.
type GmailOAuthConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

type NetworkConfig struct {
	IMAP IMAPConfig `yaml:"imap"`
}

type IMAPConfig struct {
	// IMAP connection settings will be configured per-user via DM commands
	// rather than in the global config file
	DefaultTimeout            int `yaml:"default_timeout"`
	StartupBackfillSeconds    int `yaml:"startup_backfill_seconds"`
	StartupBackfillMax        int `yaml:"startup_backfill_max"`
	InitialIdleTimeoutSeconds int `yaml:"initial_idle_timeout_seconds"`
}

type LoggingConfig struct {
	// When true, redact PII from logs using a global sanitizer hook.
	Sanitized       bool   `yaml:"sanitized"`
	PseudonymSecret string `yaml:"pseudonym_secret"`
}

// ProcessingConfig holds limits and behaviors for email → Matrix conversion
// Default values are defined once below and applied at connector startup.
const DefaultMaxUploadBytes = 25 * 1024 * 1024 // 25 MiB

type ProcessingConfig struct {
	// Maximum size in bytes for a single media upload. Set 0 to disable the check.
	MaxUploadBytes int  `yaml:"max_upload_bytes"`
	// If true, attempt gzip for oversized original HTML/text bodies before attaching.
	GzipLargeBodies bool `yaml:"gzip_large_bodies"`
}

func upgradeConfig(helper up.Helper) {
	// Copy all keys that exist in the embedded example (pkg/connector/example-config.yaml)
	
	// IMAP configuration
	helper.Copy(up.Int, "imap", "default_timeout")
	helper.Copy(up.Int, "imap", "startup_backfill_seconds") 
	helper.Copy(up.Int, "imap", "startup_backfill_max")
	helper.Copy(up.Int, "imap", "initial_idle_timeout_seconds")
	
	// Email processing configuration
	helper.Copy(up.Int, "email_processing", "max_upload_bytes")
	helper.Copy(up.Bool, "email_processing", "gzip_large_bodies")
	
	// Logging configuration
	helper.Copy(up.Bool, "logging", "sanitized")
	helper.Copy(up.Str, "logging", "pseudonym_secret")

	// Gmail OAuth (Desktop app client credentials for the device-code flow).
	helper.Copy(up.Str, "gmail_oauth", "client_id")
	helper.Copy(up.Str, "gmail_oauth", "client_secret")
}

func (ec *EmailConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &ec.Config, &up.StructUpgrader{
		SimpleUpgrader: up.SimpleUpgrader(upgradeConfig),
		Blocks: [][]string{},
		Base: ExampleConfig,
	}
}
