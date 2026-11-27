package agentlog

import "os"

func writeInvalid(path string) error { return os.WriteFile(path, []byte("broken"), 0600) }
