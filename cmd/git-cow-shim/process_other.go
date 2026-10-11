//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

func runProcess(exe string, args, env []string, containTree bool) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
