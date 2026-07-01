package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabasePath    string
	PlaidClientID   string
	PlaidSecret     string
	PlaidEnv        string // "sandbox", "development", "production"
	ClaudeAPIKey    string
	TwilioSID       string
	TwilioToken     string
	TwilioFrom      string
	NotifyPhone     string
	NotifyEmail     string
	SendGridAPIKey string
	EncryptionKey  string // 32-byte hex-encoded key for AES-256
}

func Load() (*Config, error) {
	// Load .env file if it exists
	envPath := envFilePath()
	_ = godotenv.Load(envPath)

	cfg := &Config{
		DatabasePath:   getEnv("BUDGET_DB_PATH", defaultDBPath()),
		PlaidClientID:  os.Getenv("PLAID_CLIENT_ID"),
		PlaidSecret:    os.Getenv("PLAID_SECRET"),
		PlaidEnv:       getEnv("PLAID_ENV", "sandbox"),
		ClaudeAPIKey:   os.Getenv("CLAUDE_API_KEY"),
		TwilioSID:      os.Getenv("TWILIO_ACCOUNT_SID"),
		TwilioToken:    os.Getenv("TWILIO_AUTH_TOKEN"),
		TwilioFrom:     os.Getenv("TWILIO_FROM_NUMBER"),
		NotifyPhone:    os.Getenv("NOTIFY_PHONE"),
		NotifyEmail:    os.Getenv("NOTIFY_EMAIL"),
		SendGridAPIKey: os.Getenv("SENDGRID_API_KEY"),
		EncryptionKey:  os.Getenv("ENCRYPTION_KEY"),
	}

	if cfg.EncryptionKey == "" {
		return nil, fmt.Errorf("ENCRYPTION_KEY is required (generate with: openssl rand -hex 32)")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".budget", "budget.db")
}

func envFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".budget", ".env")
}
