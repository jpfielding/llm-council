package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	OpenRouterAPIKey string
	Port             string
	DataDir          string
	CouncilModels    []string
	Chairman         string
	TitleModel       string
	AuthToken        string
}

var defaultCouncilModels = []string{
	"openai/gpt-4o",
	"google/gemini-2.5-flash",
	"anthropic/claude-sonnet-4-5",
	"x-ai/grok-3-mini",
}

func Load() (*Config, error) {
	_ = loadDotEnv(".env")

	cfg := &Config{
		OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
		Port:             getenvDefault("PORT", "8080"),
		DataDir:          getenvDefault("DATA_DIR", "./data"),
		Chairman:         getenvDefault("CHAIRMAN_MODEL", "anthropic/claude-sonnet-4-5"),
		TitleModel:       getenvDefault("TITLE_MODEL", "google/gemini-2.5-flash"),
		AuthToken:        os.Getenv("AUTH_TOKEN"),
	}

	if raw := os.Getenv("COUNCIL_MODELS"); raw != "" {
		parts := strings.Split(raw, ",")
		cfg.CouncilModels = cfg.CouncilModels[:0]
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.CouncilModels = append(cfg.CouncilModels, p)
			}
		}
	}
	if len(cfg.CouncilModels) == 0 {
		cfg.CouncilModels = append([]string(nil), defaultCouncilModels...)
	}

	if cfg.OpenRouterAPIKey == "" {
		return nil, errors.New("OPENROUTER_API_KEY is required (set in .env or environment)")
	}
	if cfg.Chairman == "" {
		return nil, errors.New("CHAIRMAN_MODEL is empty")
	}
	return cfg, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf(".env line %d: missing '='", lineNum)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}
