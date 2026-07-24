package process

import (
	"fmt"
	"net"
)

// FreePort returns a TCP port number guaranteed to be free at the instant
// of the call, by binding to port 0, reading the assigned port, then closing
// the listener.
//
// NOTE: There is a tiny race window between closing the listener and the child
// binding to the port (specification §7.1, acknowledged as a known limitation).
// This is acceptable for the intended use.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := 0
	if addr, ok := l.Addr().(*net.TCPAddr); ok {
		port = addr.Port
	}
	l.Close()
	if port == 0 {
		return 0, fmt.Errorf("could not determine port")
	}
	return port, nil
}

// SanitizeChildArgs removes any `--port <value>` (space-delimited) or
// `--port=<value>` (equals-delimited) tokens from the child argv, per
// specification §7.2. The remaining args are returned without modification.
//
// This is necessary because explicit CLI args override the LLAMA_ARG_PORT
// environment variable that loopguard sets for the child.
func SanitizeChildArgs(args []string) []string {
	out := make([]string, 0, len(args))
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--port" {
			// Drop the next token as well (the value).
			skipNext = true
			continue
		}
		if len(a) > len("--port") && a[:len("--port")] == "--port" {
			// Covers "--port=..." and "--port".
			continue
		}
		out = append(out, a)
	}
	return out
}
