package binding

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeletionRequiresFreshIdentityProof(t *testing.T) {
	for _, name := range []string{"deleted", "disabled", "active", "wrong_id", "replayed", "wrong_token", "wrong_version", "malformed", "oversized", "401", "403", "404", "410", "500"} {
		t.Run(name, func(t *testing.T) {
			token := rand.Text()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					MachineID int    `json:"machine_id"`
					Token     string `json:"token"`
					Nonce     string `json:"nonce"`
				}
				if r.Method != "POST" || r.URL.Path != "/api/v2/server/machine/binding-status" || r.URL.RawQuery != "" {
					t.Error("invalid binding request")
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || req.Token != token || req.MachineID != 117 || len(req.Nonce) != 64 {
					t.Error("invalid binding authentication")
				}
				var status int
				if _, err := fmt.Sscanf(name, "%d", &status); err == nil {
					w.WriteHeader(status)
					return
				}
				if name == "malformed" {
					fmt.Fprint(w, "not-json")
					return
				}
				if name == "oversized" {
					w.Write(make([]byte, 4097))
					return
				}
				state, id, nonce, version, signingToken := "deleted", req.MachineID, req.Nonce, 1, token
				switch name {
				case "disabled", "active":
					state = name
				case "wrong_id":
					id++
				case "replayed":
					nonce = "previous-request"
				case "wrong_token":
					signingToken = rand.Text()
				case "wrong_version":
					version++
				}
				key := sha256.Sum256([]byte(signingToken))
				mac := hmac.New(sha256.New, key[:])
				fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n%d\n%s", id, nonce)
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": version, "state": state, "machine_id": id, "nonce": nonce, "proof": hex.EncodeToString(mac.Sum(nil))}})
			}))
			defer server.Close()
			deleted, err := Deleted(context.Background(), server.URL, 117, token)
			if err != nil || deleted != (name == "deleted") {
				t.Fatalf("deleted=%v, err=%v", deleted, err)
			}
		})
	}
}

func TestDeletionRefusesRedirectsAndHonorsCancellation(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	if deleted, err := Deleted(context.Background(), server.URL, 117, rand.Text()); deleted || err != nil || forwarded.Load() {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if deleted, err := Deleted(ctx, server.URL, 117, rand.Text()); deleted || err == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation not honored")
	}
}
