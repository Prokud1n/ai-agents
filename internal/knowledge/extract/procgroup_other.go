//go:build !unix

package extract

import "os/exec"

// killProcessGroup keeps exec's default on other systems: only the child itself is killed on cancel.
func killProcessGroup(*exec.Cmd) {}
