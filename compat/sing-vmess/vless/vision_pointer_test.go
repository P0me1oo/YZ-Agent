package vless

import (
	"crypto/tls"
	"net"
	"runtime"
	"testing"

	"github.com/sagernet/sing/common/logger"
)

func TestVisionTLSPointerSurvivesGC(t *testing.T) {
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	secured := tls.Client(client, &tls.Config{ServerName: "vision.test"})
	vision, err := NewVisionConn(secured, secured, [16]byte{}, logger.NOP())
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	if vision.input.Len() != 0 || vision.rawInput.Len() != 0 {
		t.Fatal("new TLS connection unexpectedly contains buffered input")
	}
	runtime.KeepAlive(secured)
}
