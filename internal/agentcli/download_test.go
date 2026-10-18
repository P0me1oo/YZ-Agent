package agentcli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type downloadRoundTrip func(*http.Request) (*http.Response, error)

func (f downloadRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := f(req)
	if resp != nil && resp.Request == nil {
		resp.Request = req
	}
	return resp, err
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
		if strings.HasSuffix(req.URL.Path, "/SHA256SUMS") {
			return downloadTestResponse(testReleaseChecksums()), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("complete")), Header: make(http.Header)}, nil
	})}
	d := &releaseDownloader{client: client, mirrors: []string{"https://gh-proxy.org/"}, probeTimeout: time.Second}
	if err := d.download(url, dest); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 3 || requested[0] != url || requested[2] != "https://gh-proxy.org/"+url {
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
	url := resolveDownloadURL("yz-agent-linux-amd64", "v1.19.1")
	dest := filepath.Join(t.TempDir(), "agent")
	requests := 0
	client := &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header)}, nil
	})}
	d := &releaseDownloader{client: client, mirrors: releaseMirrors, probeTimeout: time.Second}
	if err := d.download(url, dest); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP failure, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("HTTP error triggered %d requests", requests)
	}
}

func TestMissingChecksumsStillAllowsArtifactMirrorSelection(t *testing.T) {
	var mu sync.Mutex
	checksumHosts := make(map[string]bool)
	sampled := false
	d := &releaseDownloader{mirrors: []string{"https://one.test/", "https://two.test/", "https://three.test/"}, probeTimeout: time.Second}
	d.client = &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/SHA256SUMS") {
			mu.Lock()
			checksumHosts[req.URL.Host] = true
			mu.Unlock()
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header)}, nil
		}
		if req.URL.Host == "github.com" {
			return nil, context.DeadlineExceeded
		}
		if req.Header.Get("Range") == "bytes=0-4095" {
			mu.Lock()
			sampled = true
			mu.Unlock()
		}
		return downloadTestResponse("complete"), nil
	})}
	dir := t.TempDir()
	err := d.downloadChecksums(resolveDownloadURL("SHA256SUMS", "v2.18.0"), filepath.Join(dir, "SHA256SUMS"))
	if !errors.Is(err, errReleaseChecksumsUnavailable) {
		t.Fatalf("未识别清单缺失：%v", err)
	}
	dest := filepath.Join(dir, "agent")
	if err := d.download(resolveDownloadURL("yz-agent-linux-amd64", "v2.18.0"), dest); err != nil {
		t.Fatal(err)
	}
	requireFileContents(t, dest, "complete")
	mu.Lock()
	defer mu.Unlock()
	if len(checksumHosts) != 4 || !sampled {
		t.Fatalf("清单来源=%v，直接探测程序=%v", checksumHosts, sampled)
	}
}

func downloadTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func testReleaseChecksums() string {
	return fmt.Sprintf("%x  yz-agent-linux-amd64\n", sha256.Sum256([]byte("complete")))
}

func waitDownloadProbe(req *http.Request, delay time.Duration) error {
	select {
	case <-time.After(delay):
		return nil
	case <-req.Context().Done():
		return req.Context().Err()
	}
}

func TestReleaseTimeoutRecoversLatestChecksumsAndBinary(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	var actualDownloads []string
	client := &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, req.URL.String())
		if strings.HasSuffix(req.URL.Path, "/yz-agent-linux-amd64") && req.Header.Get("Range") == "" {
			actualDownloads = append(actualDownloads, req.URL.String())
		}
		mu.Unlock()
		if req.URL.Host == "github.com" {
			return nil, context.DeadlineExceeded
		}
		if req.URL.Host == "offline.test" {
			return nil, waitDownloadProbe(req, time.Second)
		}
		if req.URL.Host == "slow.test" {
			if err := waitDownloadProbe(req, 15*time.Millisecond); err != nil {
				return nil, err
			}
		}
		if strings.Contains(req.URL.Path, "api.github.com") {
			return downloadTestResponse(`{"tag_name":"v2.18.0","draft":false,"prerelease":false}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/SHA256SUMS") {
			return downloadTestResponse(testReleaseChecksums()), nil
		}
		if !strings.Contains(req.URL.Path, "/download/v2.18.0/") {
			t.Errorf("附件未固定到已解析版本：%s", req.URL)
		}
		return downloadTestResponse("complete"), nil
	})}
	d := &releaseDownloader{client: client, mirrors: []string{"https://slow.test/", "https://offline.test/", "https://fast.test/"}, probeTimeout: 100 * time.Millisecond}
	target, err := d.latestStableRelease(context.Background())
	if err != nil || target != "v2.18.0" {
		t.Fatalf("target=%q err=%v", target, err)
	}
	dir := t.TempDir()
	checksumsPath := filepath.Join(dir, "SHA256SUMS")
	if err := d.download(resolveDownloadURL("SHA256SUMS", target), checksumsPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(checksumsPath)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		dest := filepath.Join(dir, "agent")
		if err := d.download(resolveDownloadURL("yz-agent-linux-amd64", target), dest); err != nil {
			t.Fatal(err)
		}
		if err := verifyReleaseChecksum(dest, "yz-agent-linux-amd64", data); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	direct := 0
	for _, url := range requests {
		if strings.HasPrefix(url, "https://github.com/") {
			direct++
		}
	}
	for _, url := range actualDownloads {
		if !strings.HasPrefix(url, "https://fast.test/") {
			t.Errorf("未选择最快的可用源：%s", url)
		}
	}
	if len(actualDownloads) != 2 {
		t.Fatalf("实际下载次数=%d", len(actualDownloads))
	}
	if direct != 1 {
		t.Fatalf("超时后重复请求直连 %d 次", direct)
	}
}

func TestDownloadRetriesNextMirrorAfterSelectedSourceFails(t *testing.T) {
	var downloads []string
	d := &releaseDownloader{mirrors: []string{"https://slow.test/", "https://fast.test/"}, probeTimeout: time.Second, mirrorOnly: true}
	d.client = &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Range") != "" {
			if req.URL.Host == "slow.test" {
				if err := waitDownloadProbe(req, 20*time.Millisecond); err != nil {
					return nil, err
				}
			}
			return downloadTestResponse("complete"), nil
		}
		downloads = append(downloads, req.URL.Host)
		if req.URL.Host == "fast.test" {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("failed")), Header: make(http.Header)}, nil
		}
		return downloadTestResponse("complete"), nil
	})}
	dest := filepath.Join(t.TempDir(), "agent")
	if err := d.download(resolveDownloadURL("yz-agent-linux-amd64", "v2.18.0"), dest); err != nil {
		t.Fatal(err)
	}
	if strings.Join(downloads, ",") != "fast.test,slow.test" {
		t.Fatalf("回退顺序：%v", downloads)
	}
	requireFileContents(t, dest, "complete")
}

func TestMirrorsRejectInvalidResponsesAndKeepExistingFile(t *testing.T) {
	for _, body := range []string{"<html>Challenge</html>", `{"tag_name":"v2.18.0-beta.1","draft":false,"prerelease":true}`, `{"tag_name":"v2.18.0"}`, strings.Repeat("x", (1<<20)+1)} {
		t.Run(fmt.Sprintf("%d-%s", len(body), body[:min(12, len(body))]), func(t *testing.T) {
			d := &releaseDownloader{mirrors: []string{"https://invalid.test/"}, probeTimeout: time.Second, mirrorOnly: true,
				client: &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
					return downloadTestResponse(body), nil
				})}}
			if _, err := d.latestStableRelease(context.Background()); err == nil {
				t.Fatal("无效版本响应被接受")
			}
			dest := filepath.Join(t.TempDir(), "agent")
			if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := d.downloadChecksums(resolveDownloadURL("SHA256SUMS", "v2.18.0"), dest); err == nil {
				t.Fatal("无效校验响应被接受")
			}
			requireFileContents(t, dest, "old")
			if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("留下部分下载文件：%v", err)
			}
		})
	}
}

func TestLatestDirectSuccessDoesNotProbeMirrors(t *testing.T) {
	calls := 0
	d := &releaseDownloader{client: &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(req.URL.Path, "/latest") {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {downloadBase + "/tag/v2.18.0"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		resp := downloadTestResponse("")
		resp.Request = req
		return resp, nil
	})}, mirrors: releaseMirrors, probeTimeout: time.Second}
	target, err := d.latestStableRelease(context.Background())
	if err != nil || target != "v2.18.0" || calls != 2 {
		t.Fatalf("target=%q calls=%d err=%v", target, calls, err)
	}
}

func TestLatestDoesNotFallbackOnHTTPErrorOrCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		calls := 0
		d := &releaseDownloader{client: &http.Client{Transport: downloadRoundTrip(func(req *http.Request) (*http.Response, error) {
			calls++
			if cancelled {
				return nil, context.Canceled
			}
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("denied")), Header: make(http.Header)}, nil
		})}, mirrors: releaseMirrors, probeTimeout: time.Second}
		if _, err := d.latestStableRelease(context.Background()); err == nil || calls != 1 {
			t.Fatalf("非超时错误触发了镜像回退：calls=%d err=%v", calls, err)
		}
	}
}
