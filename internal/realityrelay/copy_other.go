//go:build !linux

package realityrelay

func spliceSupported() bool { return false }
