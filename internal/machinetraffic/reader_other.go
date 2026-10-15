//go:build !linux

package machinetraffic

import "errors"

func Read([]string) (Reading, error) {
	return Reading{}, errors.New("machine traffic requires Linux host network counters")
}
