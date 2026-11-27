// Package binding verifies explicit deletion notices before removing local bindings.
package binding

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Proof struct {
	Version   int    `json:"version"`
	MachineID int    `json:"machine_id"`
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	MAC       string `json:"proof"`
}

// Confirmation is scoped to the configured panel, machine and credential.
// Only Verify can construct a nonzero confirmation.
type Confirmation struct {
	panel string
	id    int
	key   [sha256.Size]byte
}

func (c Confirmation) Matches(panel string, id int, token string) bool {
	return c.id > 0 && c.id == id && c.panel == strings.TrimRight(panel, "/") && c.key == sha256.Sum256([]byte(token))
}

func Challenge(panelURL string) string {
	u, err := url.Parse(panelURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip.IsLoopback())) {
		return ""
	}
	var random [32]byte
	rand.Read(random[:])
	return hex.EncodeToString(random[:])
}

func (p *Proof) Verify(panel string, id int, token, nonce string) (Confirmation, bool) {
	if p == nil || p.Version != 1 || p.State != "deleted" {
		return Confirmation{}, false
	}
	if id <= 0 || token == "" || p.MachineID != id || len(nonce) != 64 || p.Nonce != nonce {
		return Confirmation{}, false
	}
	proof, err := hex.DecodeString(p.MAC)
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n%d\n%s", id, nonce)
	if err != nil || !hmac.Equal(proof, mac.Sum(nil)) {
		return Confirmation{}, false
	}
	return Confirmation{panel: strings.TrimRight(panel, "/"), id: id, key: key}, true
}

// During startup and installation the machine runtime may not yet be able to
// decrypt its config. Discovery works with just the public binding envelope.
func Deleted(ctx context.Context, panelURL string, id int, token string) (bool, error) {
	nonce := Challenge(panelURL)
	if nonce == "" || id <= 0 || token == "" {
		return false, nil
	}
	body, _ := json.Marshal(map[string]any{"machine_id": id, "token": token, "nonce": nonce})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(panelURL, "/")+"/api/v2/server/machine/nodes", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false, errors.New("绑定状态暂时无法确认，保留本机配置")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	var result struct {
		Deletion *Proof `json:"binding_deletion"`
	}
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &result) != nil {
		return false, nil
	}
	_, ok := result.Deletion.Verify(panelURL, id, token, nonce)
	return ok, nil
}
