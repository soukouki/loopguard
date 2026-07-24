// Command loopguard is a thin streaming proxy for llama-server that detects
// output loops and cuts the stream early, per the loopguard specification.
//
// Usage:
//
//	loopguard [--port ...] [--child-port ...] [--loop-threshold-bytes ...] -- <child-command> [<args>...]
//
// The `--` separator (specification §2.1) divides loopguard flags from the
// child process command line.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/sou7/loopguard/internal/config"
	"github.com/sou7/loopguard/internal/process"
	"github.com/sou7/loopguard/internal/proxy"
)

// defaultMinPeriodBytes and defaultMaxPeriodBytes bound the KMP period search.
// These are hardcoded per specification §2.2 and §3 (not flags).
const (
	defaultMinPeriodBytes = 1
	defaultMaxPeriodBytes = 4000
)

// shutdownWaitTimeout is the fixed grace period before SIGKILL when
// shutting down the child process (specification §8, "5秒固定").
const shutdownWaitTimeout = 5 * time.Second

func main() {
	// Parse flags + child command line, split by the `--` separator.
	cfg, err := config.ParseFromOsArgs()
	if err != nil {
		log.Fatalf("loopguard: %v", err)
	}

	// Resolve the internal child port (specification §7).
	childPort := cfg.ChildPort
	if !cfg.HasChildPort() {
		childPort, err = process.FreePort()
		if err != nil {
			log.Fatalf("loopguard: could not acquire child port: %v", err)
		}
	}

	// Strip any explicit --port tokens from the child argv so that the
	// LLAMA_ARG_PORT environment variable (which we set below) takes effect
	// (specification §7.2). CLI args override env vars in llama-server.
	sanitizedArgs := process.SanitizeChildArgs(cfg.ChildArgs)

	// Prepare the child command.
	childCmd := exec.Command(cfg.ChildCmd, sanitizedArgs...)

	// Set LLAMA_ARG_PORT so the child binds to the reserved internal port.
	var env []string = os.Environ()
	childCmd.Env = append(env, "LLAMA_ARG_PORT="+strconv.Itoa(childPort))
	childCmd.Stdout = os.Stdout // §8: pipe child stdout/stderr to loopguard.
	childCmd.Stderr = os.Stderr
	childCmd.Stdin = nil

	// Start the child process.
	if err := childCmd.Start(); err != nil {
		log.Fatalf("loopguard: failed to start child: %v", err)
	}
	childPid := childCmd.Process.Pid
	log.Printf("loopguard: started child %q (pid=%d) on internal port %d",
		cfg.ChildCmd, childPid, childPort)

	// Build the proxy targeting the child's internal port.
	p := proxy.New(childPort, defaultMinPeriodBytes, defaultMaxPeriodBytes, cfg.LoopThreshold)

	// Set up an HTTP server on the external port (what llama-swap expects).
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", cfg.Port),
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		ErrorLog:          log.Default(),
	}

	// Start the HTTP server in a goroutine.
	go func() {
		log.Printf("loopguard: listening on :%d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("loopguard: server error: %v", err)
		}
	}()

	// --- Process management (specification §8) ---

	// Wait for OS signals to propagate to the child.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Goroutine: forward the child's exit so we can mirror its exit code.
	childExitCh := make(chan error, 1)
	go func() {
		childExitCh <- childCmd.Wait()
	}()

	select {
	case sig := <-sigCh:
		log.Printf("loopguard: received signal %v, forwarding to child", sig)
		_ = childCmd.Process.Signal(sig) // Forward SIGTERM/SIGINT.

		// Wait up to shutdownWaitTimeout for the child to exit gracefully.
		done := make(chan struct{})
		go func() {
			childCmd.Wait()
			close(done)
		}()

		select {
		case <-done:
			log.Println("loopguard: child exited after signal")
		case <-time.After(shutdownWaitTimeout):
			log.Println("loopguard: child did not exit in time, sending SIGKILL")
			_ = childCmd.Process.Signal(syscall.SIGKILL)
		}

	case err := <-childExitCh:
		// Child exited unexpectedly (before loopguard received a signal).
		// Mirror its exit code (specification §8).
		log.Printf("loopguard: child exited unexpectedly: %v", err)
		srv.Shutdown(context.Background())
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
		os.Exit(0)
	}
}
