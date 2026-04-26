package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvRespectsExistingEnv(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("FOO=from_file\nBAR=\"quoted\"\n# comment\nBAZ=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOO", "from_env")
	t.Setenv("BAR", "")
	t.Setenv("BAZ", "")

	if err := loadDotEnv(envFile); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}

	if got := os.Getenv("FOO"); got != "from_env" {
		t.Errorf("FOO: expected real env to win, got %q", got)
	}
	if got := os.Getenv("BAR"); got != "quoted" {
		t.Errorf("BAR: expected quotes stripped, got %q", got)
	}
	if got := os.Getenv("BAZ"); got != "x" {
		t.Errorf("BAZ: got %q", got)
	}
}

func TestLoadDotEnvMissingIsNoError(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("missing .env should not error, got %v", err)
	}
}

func TestLoadDotEnvMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("THIS_LINE_HAS_NO_EQUALS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(envFile); err == nil {
		t.Error("expected error for malformed line")
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	// Isolate: clear OPENROUTER_API_KEY in this subprocess and point to empty dir
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("COUNCIL_MODELS", "")
	// Change to a tmp dir so .env isn't found
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error when OPENROUTER_API_KEY is empty")
	}
}

func TestLoadParsesCouncilModels(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("COUNCIL_MODELS", " model/a , model/b ,, model/c ")
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"model/a", "model/b", "model/c"}
	if len(cfg.CouncilModels) != len(want) {
		t.Fatalf("got %d models, want %d: %v", len(cfg.CouncilModels), len(want), cfg.CouncilModels)
	}
	for i, m := range want {
		if cfg.CouncilModels[i] != m {
			t.Errorf("idx %d: got %q want %q", i, cfg.CouncilModels[i], m)
		}
	}
}
