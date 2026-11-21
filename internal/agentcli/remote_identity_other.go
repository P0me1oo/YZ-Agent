//go:build !linux

package agentcli

func openRCManagedProcess(string) bool { return false }
