//go:build !linux

package agentcli

import "errors"

func lockRemoteExecution() (func(), error) { return nil, errors.New("remote operations require Linux") }
