package xray

import (
	"bytes"
	"testing"
	"time"

	hy "github.com/xtls/xray-core/proxy/hysteria"
)

func TestHysteriaAddresslessFragmentsOutOfOrder(t *testing.T) {
	reader := &hysteriaFragmentReader{pending: make(map[uint16]*hysteriaFragments)}
	now := time.Now()
	for _, id := range []uint8{2, 1, 1, 0} {
		message := hy.UDPMessage{PacketID: 7, FragID: id, FragCount: 3, Data: []byte{byte('a' + id)}}
		if id == 0 {
			message.Addr = "[::1]:53"
		}
		raw := make([]byte, message.Size())
		message.Serialize(raw)
		parsed, err := parseHysteriaFragment(raw)
		if err != nil {
			t.Fatal(err)
		}
		data, address := reader.feed(parsed, now)
		if id != 0 && data != nil {
			t.Fatal("released partial UDP")
		}
		if id == 0 && (!bytes.Equal(data, []byte("abc")) || address != "[::1]:53") {
			t.Fatal("incorrect reassembly")
		}
	}
	if len(reader.pending) != 0 {
		t.Fatal("completed fragments retained")
	}
}
func TestHysteriaFragmentBoundsExpiryAndAddressConflicts(t *testing.T) {
	reader := &hysteriaFragmentReader{pending: make(map[uint16]*hysteriaFragments)}
	now := time.Now()
	for i := 0; i < 100; i++ {
		reader.feed(&hy.UDPMessage{PacketID: uint16(i), FragID: 0, FragCount: 2, Addr: "a.test:53", Data: []byte("part")}, now)
	}
	if len(reader.pending) != 64 {
		t.Fatal("unbounded incomplete packets")
	}
	reader.feed(&hy.UDPMessage{PacketID: 100, FragID: 0, FragCount: 2, Addr: "a.test:53", Data: []byte("part")}, now.Add(6*time.Second))
	if len(reader.pending) != 1 {
		t.Fatal("expired packets retained")
	}
	if data, _ := reader.feed(&hy.UDPMessage{PacketID: 100, FragID: 1, FragCount: 2, Addr: "b.test:53", Data: []byte("part")}, now.Add(6*time.Second)); data != nil {
		t.Fatal("mixed destinations accepted")
	}
	for _, raw := range [][]byte{nil, make([]byte, 8), make([]byte, 9)} {
		if _, err := parseHysteriaFragment(raw); err == nil {
			t.Fatal("invalid header accepted")
		}
	}
}
