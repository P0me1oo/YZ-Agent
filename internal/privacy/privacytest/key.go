package privacytest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

// 测试密钥仅在当前测试进程中存在。
func Bind(t testing.TB, dir string) *privacy.State {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	s, err := privacy.New([]privacy.Key{{ID: hex.EncodeToString(sum[:16]), Value: base64.StdEncoding.EncodeToString(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	privacy.Bind(dir, s, nil, true)
	t.Cleanup(func() { privacy.Bind(dir, nil, nil, true) })
	return s
}
