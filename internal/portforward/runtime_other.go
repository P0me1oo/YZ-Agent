//go:build !linux

package portforward

import (
	"context"
	"errors"
	"os"
)

type commandFunc func(context.Context, string, []string, string) ([]byte, error)

func runCommand(context.Context, string, []string, string) ([]byte, error) {
	return nil, errors.New("port forwarding requires Linux")
}
func ipv6Unavailable(error) bool { return false }
func lockDirectory(string) (*os.File, error) {
	return nil, errors.New("port forwarding requires Linux")
}

type realmProcess struct{ rule resolvedRule }

func (*realmProcess) alive() bool { return false }
func (*realmProcess) stop()       {}
func (*Manager) startRealm(context.Context, resolvedRule) (*realmProcess, error) {
	return nil, errors.New("port forwarding requires Linux")
}
func (*Manager) applyNFT(context.Context, []resolvedRule) error {
	return errors.New("port forwarding requires Linux")
}

func nftConflicts([]byte, string, []resolvedRule) error {
	return errors.New("NAT inspection requires Linux")
}
