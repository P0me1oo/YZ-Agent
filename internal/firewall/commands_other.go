//go:build !linux

package firewall

import "os/exec"

func configureCommandCancellation(_ *exec.Cmd) {}
