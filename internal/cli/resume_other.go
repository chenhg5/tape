//go:build !unix

package cli

import (
	"os"
	"os/exec"
)

// execAgent on non-unix platforms (Windows, plan9, …) where there is
// no clean "replace the current process" primitive. We spawn the agent
// as a child, wire stdio through, and propagate its exit code. The
// behavior is one process deeper but functionally indistinguishable
// for the user — tape is just the parent now.
func execAgent(bin string, args, env []string) error {
	c := exec.Command(bin, args[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = env
	return c.Run()
}
