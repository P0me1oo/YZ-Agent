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

// Deleted accepts only a fresh proof for this exact machine and credential.
// Generic 401/403/404/410 responses never authorize removal.
func Deleted(ctx context.Context, panelURL string, id int, token string) (bool, error) {
	u, err := url.Parse(panelURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && u.Scheme != "http") || id <= 0 || token == "" {
		return false, nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		// Never send the proof key over a public plaintext connection.
		return false, nil
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, err
	}
	nonce := hex.EncodeToString(random[:])
	body, _ := json.Marshal(map[string]any{"machine_id": id, "token": token, "nonce": nonce})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(panelURL, "/")+"/api/v2/server/machine/binding-status", bytes.NewReader(body))
	if err != nil {
		return false, errors.New("无法创建绑定状态请求")
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
	if err != nil || len(data) > 4096 {
		return false, nil
	}
	var result struct {
		Data struct {
			Version   int    `json:"version"`
			MachineID int    `json:"machine_id"`
			State     string `json:"state"`
			Nonce     string `json:"nonce"`
			Proof     string `json:"proof"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil || result.Data.Version != 1 || result.Data.MachineID != id ||
		result.Data.State != "deleted" || result.Data.Nonce != nonce {
		return false, nil
	}
	proof, err := hex.DecodeString(result.Data.Proof)
	if err != nil {
		return false, nil
	}
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n%d\n%s", id, nonce)
	return hmac.Equal(proof, mac.Sum(nil)), nil
}
