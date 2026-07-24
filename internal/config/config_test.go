package config

import (
	"strings"
	"testing"
)

func TestParse_HelpFlag(t *testing.T) {
	// --help should exit with code 0 and print usage to stderr.
	// We can't call Parse directly (it calls os.Exit), so we verify
	// the usage text constant contains expected sections.
	if !strings.Contains(usageText, "USAGE") || !strings.Contains(usageText, "--port") {
		t.Error("usageText does not contain expected sections")
	}
}

func TestParse_SplitsAtSeparator(t *testing.T) {
	cfg, err := Parse([]string{
		"--port", "8080",
		"--",
		"llama-server", "/app/models/x.gguf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.ChildCmd != "llama-server" {
		t.Errorf("ChildCmd = %q", cfg.ChildCmd)
	}
	wantArgs := []string{"/app/models/x.gguf"}
	if len(cfg.ChildArgs) != len(wantArgs) || cfg.ChildArgs[0] != wantArgs[0] {
		t.Errorf("ChildArgs = %v, want %v", cfg.ChildArgs, wantArgs)
	}
}

func TestParse_MissingSeparator(t *testing.T) {
	_, err := Parse([]string{"--port", "8080"})
	if err == nil {
		t.Fatal("expected error for missing separator")
	}
}

func TestParse_EmptyChildCommand(t *testing.T) {
	_, err := Parse([]string{"--port", "8080", "--"})
	if err == nil {
		t.Fatal("expected error for empty child command")
	}
}

func TestParse_RequiredPort(t *testing.T) {
	_, err := Parse([]string{"--", "llama-server"})
	if err == nil {
		t.Fatal("expected error for missing --port")
	}
}

func TestParse_EqualsPortStyle(t *testing.T) {
	cfg, err := Parse([]string{
		"--port=8080",
		"--",
		"llama-server",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestLoopThresholdBytesDefault(t *testing.T) {
	cfg, err := Parse([]string{"--port", "8080", "--", "llama-server"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoopThreshold != 500 {
		t.Errorf("LoopThreshold = %d, want 500", cfg.LoopThreshold)
	}
}
