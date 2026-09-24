package agentcli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type downloadRoundTrip func(*http.Request) (*http.Response, error)

func (f downloadRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type interruptedDownload struct{ sent bool }

func (r *interruptedDownload) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial"), nil
	}
	return 0, context.DeadlineExceeded
}

func TestDownloadFallsBackAfterBodyTimeout(t *testing.T) {
	url := resolveDownloadURL("yz-agent-linux-amd64", "v1.19.1")
	dest := filepath.Join(t.TempDir(), "agent")
	var requested []string
	client := &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		requested = append(requested, req.URL.String())
		if len(requested) == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&interruptedDownload{}), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("complete")), Header: make(http.Header)}, nil
	})}
	if err := downloadFileWithFallback(client, url, dest); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 || requested[0] != url || requested[1] != "https://gh-proxy.org/"+url {
		t.Fatalf("unexpected download order: %v", requested)
	}
	content, err := os.ReadFile(dest)
	if err != nil || string(content) != "complete" {
		t.Fatalf("downloaded content = %q, error = %v", content, err)
	}
	if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial download was retained: %v", err)
	}
}

func TestDownloadDoesNotFallbackOnHTTPError(t *testing.T) {
	url := resolveDownloadURL("SHA256SUMS", "v1.19.1")
	dest := filepath.Join(t.TempDir(), "SHA256SUMS")
	requests := 0
	client := &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header)}, nil
	})}
	if err := downloadFileWithFallback(client, url, dest); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP failure, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("HTTP error triggered %d requests", requests)
	}
}
