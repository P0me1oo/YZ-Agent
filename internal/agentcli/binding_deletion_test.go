package agentcli

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestConfigInitReplacesDeletedEncryptedBindingWithoutChangingLiveFiles(t *testing.T) {
	for _, kernel := range []string{"singbox", "xray"} {
		t.Run(kernel, func(t *testing.T) {
			token, newToken := rand.Text(), rand.Text()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					MachineID int    `json:"machine_id"`
					Token     string `json:"token"`
					Nonce     string `json:"nonce"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if req.Token != token || req.MachineID != 117 || r.URL.Path != "/api/v2/server/machine/binding-status" {
					w.WriteHeader(403)
					return
				}
				key := sha256.Sum256([]byte(token))
				mac := hmac.New(sha256.New, key[:])
				fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n117\n%s", req.Nonce)
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "machine_id": 117, "state": "deleted", "nonce": req.Nonce, "proof": hex.EncodeToString(mac.Sum(nil))}})
			}))
			defer server.Close()
			dir := t.TempDir()
			live, staged := filepath.Join(dir, "config.yml"), filepath.Join(dir, "staged.yml")
			credentials, stagedCredentials := filepath.Join(dir, "credentials.env"), filepath.Join(dir, "staged.env")
			before := fmt.Sprintf("instances:\n  - panel:\n      url: %q\n    machine:\n      machine_id: 117\n      token_env: DELETED_INIT_TEST\n    protected: unavailable-ciphertext\n", server.URL)
			os.WriteFile(live, []byte(before), 0o600)
			os.WriteFile(credentials, []byte("DELETED_INIT_TEST="+token+"\n"), 0o600)
			args := []string{"--mode", "machine", "--panel-url", server.URL, "--machine-id", "139", "--kernel", kernel, "--token", newToken,
				"--config", live, "--output", staged, "--credentials-in", credentials, "--credentials-out", stagedCredentials, "--install-root", dir}
			if err := runConfigInit(args); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(live); string(data) != before {
				t.Fatal("staging changed live config")
			}
			if data, _ := os.ReadFile(credentials); string(data) != "DELETED_INIT_TEST="+token+"\n" {
				t.Fatal("staging changed live credentials")
			}
			root, err := config.LoadWritableRoot(staged)
			if err != nil || len(root.Instances) != 1 || root.Instances[0].Machine.MachineID != 139 || root.Instances[0].Kernel.Type != kernel {
				t.Fatal("new binding incorrect", err)
			}
			creds, _ := os.ReadFile(stagedCredentials)
			if strings.Contains(string(creds), "DELETED_INIT_TEST") || !strings.Contains(string(creds), newToken) {
				t.Fatal("staged credentials incorrect")
			}
		})
	}
}
