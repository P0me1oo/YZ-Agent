package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDeletedBindingProcessWaitsAndAcceptsRebinding(t *testing.T) {
	token := rand.Text()
	var deleteNew atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MachineID int    `json:"machine_id"`
			Token     string `json:"token"`
			Nonce     string `json:"nonce"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Token != token {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/api/v2/server/machine/binding-status":
			if req.MachineID == 117 || deleteNew.Load() {
				key := sha256.Sum256([]byte(token))
				mac := hmac.New(sha256.New, key[:])
				fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n%d\n%s", req.MachineID, req.Nonce)
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "machine_id": req.MachineID, "state": "deleted", "nonce": req.Nonce, "proof": hex.EncodeToString(mac.Sum(nil))}})
			} else {
				fmt.Fprintf(w, `{"data":{"version":1,"machine_id":%d,"state":"active"}}`, req.MachineID)
			}
		case "/api/v2/server/machine/privacy":
			fmt.Fprint(w, `{"data":{"version":1,"enabled":false,"keys":[]}}`)
		case "/api/v2/server/machine/nodes":
			fmt.Fprint(w, `{"nodes":[],"base_config":{"pull_interval":1,"push_interval":60}}`)
		case "/api/v2/server/machine/control":
			fmt.Fprint(w, `{}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	writeBinding := func(id int) {
		t.Helper()
		data := fmt.Sprintf("panel:\n  url: %q\nmachine:\n  machine_id: %d\n  token: %q\nhealth_port: %d\nfirewall:\n  enabled: false\ntime_sync:\n  enabled: false\nkernel:\n  type: singbox\n", server.URL, id, token, port)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeBinding(117)
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAgentCommandEntrypoint$", "--", "run", "-c", path)
	cmd.Env = append(os.Environ(), "YZ_AGENT_ENTRYPOINT_TEST=1")
	logFile, err := os.Create(filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()
	waitHealth := func(status string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		client := &http.Client{Timeout: time.Second}
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				t.Fatalf("agent exited: %v", err)
			default:
			}
			resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
			if err == nil {
				data, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var health struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(data, &health) == nil && health.Status == status {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		output, _ := os.ReadFile(filepath.Join(dir, "output.log"))
		t.Fatalf("health did not become %s: %s", status, strings.ReplaceAll(string(output), token, "[redacted]"))
	}
	waitHealth("unbound", 5*time.Second)
	writeBinding(139)
	waitHealth("ok", 6*time.Second)
	deleteNew.Store(true)
	waitHealth("unbound", 36*time.Second)
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "139") || strings.Contains(string(data), token) {
		t.Fatal("deleted identity retained")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("idle service failed to stop")
	}
}
