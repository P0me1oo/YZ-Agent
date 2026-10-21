//go:build with_utls

package vless

import (
	"net"
	"runtime"
	"testing"

	utls "github.com/metacubex/utls"
	"github.com/sagernet/sing/common/logger"
)

func TestVisionUTLSPointerSurvivesGC(t *testing.T) {
	for _, browser := range []bool{false, true} {
		name := "TLS"
		if browser {
			name = "UClient"
		}
		t.Run(name, func(t *testing.T) {
			client, peer := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
			var secured net.Conn = utls.Client(client, &utls.Config{ServerName: "vision.test"})
			if browser {
				secured = utls.UClient(client, &utls.Config{ServerName: "vision.test"}, utls.HelloChrome_Auto)
			}
			vision, err := NewVisionConn(secured, secured, [16]byte{}, logger.NOP())
			if err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			if vision.input.Len() != 0 || vision.rawInput.Len() != 0 {
				t.Fatal("new TLS connection unexpectedly contains buffered input")
			}
			runtime.KeepAlive(secured)
		})
	}
}
