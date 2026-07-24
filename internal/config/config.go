package config

import (
	"flag"
	"fmt"
	"os"
)

// Config holds loopguard configuration parsed from command-line flags.
type Config struct {
	Port          int
	ChildPort     int
	LoopThreshold int // redundant bytes threshold for loop detection
	ChildCmd      string
	ChildArgs     []string
}

const separator = "--" // ハイフン2つ、固定・変更不可

// Usage text:
const usageText = `loopguard: a streaming proxy for llama-server that detects output loops.

USAGE
  loopguard [loopguard-flags...] -- <child-command> [child-args...]

FLAGS
  --port                  (required) listen port for llama-swap
  --child-port            internal child process port (0 = auto)
  --loop-threshold-bytes   bytes of redundant repetition before cut-off (default 500)

ARGUMENTS
  --                      separates loopguard flags from the child command line
  <child-command>         the llama-server binary path (required)
  [child-args...]         optional arguments passed to the child process

EXAMPLES
  loopguard --port 8080 -- llama-server /app/model.gguf
`

// Parse parses loopguard flags and the child command split by `--`.
// The first argument matching exactly `--` separates loopguard flags from
// the child command line. Returns an error if the separator is missing.
func Parse(args []string) (*Config, error) {
	// Check for --help before the separator (it would appear in loopguard flags).
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "-help" {
			fmt.Fprint(os.Stderr, usageText)
			os.Exit(0)
		}
	}

	fs := flag.NewFlagSet("loopguard", flag.ContinueOnError)
	// Send usage text to stderr on --help or parse errors.
	fs.SetOutput(os.Stderr)
	cfg := &Config{
		ChildPort:     0, // 0 = auto
		LoopThreshold: 500,
	}
	fs.IntVar(&cfg.Port, "port", 0, "loopguard listen port (required)")
	fs.IntVar(&cfg.ChildPort, "child-port", 0, "internal child process port (0 = auto)")
	fs.IntVar(&cfg.LoopThreshold, "loop-threshold-bytes", 500, "redundant bytes before cut-off")

	// Split args at the separator `--`.
	idx := -1
	for i, a := range args {
		if a == separator {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("missing required separator %q (usage: --help)", separator)
	}

	loopguardArgs := args[:idx]
	childParts := args[idx+1:]

	if len(loopguardArgs) == 0 {
		return nil, fmt.Errorf("no loopguard flags before separator")
	}
	if len(childParts) == 0 {
		return nil, fmt.Errorf("no child command after separator")
	}

	// Parse loopguard flags.
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	if err := fs.Parse(loopguardArgs); err != nil {
		return nil, fmt.Errorf("loopguard flags: %w", err)
	}

	if cfg.Port == 0 {
		return nil, fmt.Errorf("port is required")
	}

	// Reconstruct child command line. The first token is the command, the rest are args.
	cfg.ChildCmd = childParts[0]
	cfg.ChildArgs = childParts[1:]
	return cfg, nil
}

// ParseFromOsArgs is convenience wrapper around Parse(os.Args[1:]).
func ParseFromOsArgs() (*Config, error) {
	return Parse(os.Args[1:])
}

// JoinChild returns the child command and its args as a single string slice
// suitable for use with exec.Command.
func (c *Config) JoinChild() []string {
	return append([]string{c.ChildCmd}, c.ChildArgs...)
}

// HasChildPort returns true if the child port was explicitly given.
func (c *Config) HasChildPort() bool {
	return c.ChildPort != 0
}
