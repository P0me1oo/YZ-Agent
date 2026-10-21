package agentlog

import (
	"os"
	"strings"
)

func writeInvalid(path string) error  { return os.WriteFile(path, []byte("broken"), 0600) }
func containsSecret(text string) bool { return strings.Contains(text, "temporary-test-secret") }
