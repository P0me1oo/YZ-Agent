//go:build !linux

package privacy

import "os"

func openForRead(path string) (*os.File, error) { return os.Open(path) }

func openRootForRead(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
