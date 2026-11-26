package panel

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"time"
)

// 只记录阶段时间与随机关联编号，不保留地址、请求头、正文或认证内容。
type requestTrace struct {
	mu                                    sync.Mutex
	started                               time.Time
	id                                    string
	stage                                 string
	dns, connected, tls, wrote, firstByte time.Duration
	reused                                bool
	serverMS                              float64
}

func tracePanelRequest(req *http.Request) *requestTrace {
	trace := &requestTrace{started: time.Now(), stage: "connection_wait", serverMS: -1}
	var id [8]byte
	if _, err := rand.Read(id[:]); err == nil {
		trace.id = hex.EncodeToString(id[:])
		req.Header.Set("X-YZ-Request-ID", trace.id)
	}
	mark := func(stage string, elapsed *time.Duration) {
		trace.mu.Lock()
		defer trace.mu.Unlock()
		trace.stage = stage
		if elapsed != nil {
			*elapsed = time.Since(trace.started)
		}
	}
	hooks := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { mark("dns", nil) },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			stage := "connect"
			if info.Err != nil {
				stage = "dns"
			}
			mark(stage, &trace.dns)
		},
		ConnectStart: func(string, string) { mark("connect", nil) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				mark("connected", &trace.connected)
			}
		},
		TLSHandshakeStart: func() { mark("tls", nil) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				mark("write", &trace.tls)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			trace.mu.Lock()
			trace.reused = info.Reused
			trace.stage = "write"
			trace.mu.Unlock()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				mark("response_wait", &trace.wrote)
			}
		},
		GotFirstResponseByte: func() { mark("response_headers", &trace.firstByte) },
	}
	*req = *req.WithContext(httptrace.WithClientTrace(req.Context(), hooks))
	return trace
}

func (t *requestTrace) response(resp *http.Response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stage = "response_body"
	if value, err := strconv.ParseFloat(resp.Header.Get("X-YZ-Process-Ms"), 64); err == nil && value >= 0 && value <= 3600000 {
		t.serverMS = value
	}
}

func (t *requestTrace) failure(err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &requestTimingError{cause: err, detail: fmt.Sprintf(
		"request_id=%s phase=%s total_ms=%d dns_at_ms=%d connect_at_ms=%d tls_at_ms=%d write_at_ms=%d first_byte_at_ms=%d reused=%t server_ms=%.3f",
		t.id, t.stage, time.Since(t.started).Milliseconds(), t.dns.Milliseconds(), t.connected.Milliseconds(),
		t.tls.Milliseconds(), t.wrote.Milliseconds(), t.firstByte.Milliseconds(), t.reused, t.serverMS,
	)}
}

type requestTimingError struct {
	cause  error
	detail string
}

func (e *requestTimingError) Error() string { return e.cause.Error() + " (" + e.detail + ")" }
func (e *requestTimingError) Unwrap() error { return e.cause }

type tracedPanelBody struct {
	io.ReadCloser
	trace *requestTrace
}

func (b *tracedPanelBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		return n, b.trace.failure(err)
	}
	return n, err
}

// 确保计时包装不改变取消、超时以及原有的错误类型判断。
var _ interface{ Unwrap() error } = (*requestTimingError)(nil)
