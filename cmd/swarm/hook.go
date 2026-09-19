package main

import (
	"io"
	"os"

	"github.com/AlexanderTar/agent-swarm/internal/hook"
)

// cmdHook is `swarm hook <agent> <event>`. It never blocks the agent (§11.2):
// hook.Run reads SWARM_SESSION/SWARM_TOKEN_FILE itself and exits 0 silently
// when either is unset (a session the user started by hand).
func cmdHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return hook.Run(args, stdin, stdout, os.Getenv, nil)
}
