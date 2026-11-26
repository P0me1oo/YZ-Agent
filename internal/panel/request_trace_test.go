package panel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestRequestTraceExplainsTimeoutWithoutCredentials(t *testing.T) {
	ids := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids <- r.Header.Get("X-YZ-Request-ID")
		<-r.Context().Done()
	}))
	defer server.Close()
	credential := strings.Repeat("x", 24)
	client := NewClient(config.PanelConfig{URL: server.URL, Token: credential})
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err := client.doRequestContext(ctx, "GET", "/api/v2/server/realtime/state", nil, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline chain lost: %v", err)
	}
	id := <-ids
	if !regexp.MustCompile(`^[a-f0-9]{16}$`).MatchString(id) {
		t.Fatal("missing correlation id")
	}
	text := err.Error()
	for _, value := range []string{"phase=response_wait", "request_id=" + id, "total_ms=", "write_at_ms="} {
		if !strings.Contains(text, value) {
			t.Fatalf("missing %s: %s", value, text)
		}
	}
	if strings.Contains(text, credential) || strings.Contains(text, server.URL) {
		t.Fatal("credential or origin leaked")
	}
}

func TestRequestTracePreservesBodyTimeoutAndServerTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-YZ-Process-Ms", "12.25")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL})
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	resp, err := client.doRequestContext(ctx, "POST", "/api/v2/server/realtime/state", []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "phase=response_body") || !strings.Contains(err.Error(), "server_ms=12.250") {
		t.Fatalf("body diagnostics lost: %v", err)
	}
}
