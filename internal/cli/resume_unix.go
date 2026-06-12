//go:build unix

package cli

import "syscall"

// execAgent replaces the tape process with the agent so the agent owns
// the PTY directly. This is the conventional unix "drop me into another
// program" pattern (think `git svn` shelling out, or `ssh -t … exec`):
// tape vanishes from the process tree, signals route straight to the
// agent, and the agent's exit code becomes the shell's exit code.
//
// On success this never returns. We surface only the error path.
func execAgent(bin string, args, env []string) error {
	return syscall.Exec(bin, args, env)
}
