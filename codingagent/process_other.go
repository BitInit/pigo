//go:build !unix

package codingagent

import "os/exec"

// exec.CommandContext kills the direct process on platforms without Unix groups.
func configureProcess(cmd *exec.Cmd) {}
