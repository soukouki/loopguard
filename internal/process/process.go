// Package process manages spawning and supervising the child llama-server
// process, per specification section 8.
package process

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Spawner starts the child process and returns a handle.
// The child's stdout/stderr are connected to loopguard's, per spec §8.
type Spawner struct {
	cmd *exec.Cmd
}

// Start launches the child command. The child's stdin is nil so that it
// does not receive unrelated stdin from the parent. Stdout and stderr are
// piped to loopguard's corresponding streams.
func Start(name string, args []string) (*Spawner, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Spawner{cmd: cmd}, nil
}

// Wait blocks until the child process exits and returns its exit error.
// If the context is cancelled first, the child is signalled and waited on.
func (s *Spawner) Wait(ctx context.Context) error {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- s.cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// Forward signals: spec §8 says SIGTERM/SIGINT trigger child shutdown.
		// The actual signal is handled by the caller (main) via SignalAndWait.
		<-waitDone
		return ctx.Err()
	case err := <-waitDone:
		return err
	}
}

// SignalAndWait sends a signal to the child, waits up to `waitTimeout` for it
// to exit, then SIGKILLs it if it did not terminate. This implements the
// spec §8 ("SIGTERM/SIGINT -> forward -> wait 5s -> SIGKILL").
//
// waitTimeout is a code-internal constant (5 seconds) per spec §8.
func (s *Spawner) SignalAndWait(sig os.Signal, waitTimeout time.Duration) error {
	if s.cmd.Process == nil {
		return nil
	}
	_ = s.cmd.Process.Signal(sig) //nolint:errcheck // best-effort signal

	done := make(chan error, 1)
	go func() {
		d := s.cmd.Wait()
		done <- d
	}()

	timer := time.AfterFunc(waitTimeout, func() {
		// Forceful kill after timeout.
		_ = s.cmd.Process.Signal(syscall.SIGKILL) //nolint:errcint
	})
	defer timer.Stop()

	err := <-done
	return err
}

// Pid returns the child process PID, or 0 if not started.
func (s *Spawner) Pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}
