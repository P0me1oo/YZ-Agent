//go:build !unix

package realityrelay

import "os"

func openPrivateKeyFile(path string) (*os.File, error) {
	return os.Open(path)
}
