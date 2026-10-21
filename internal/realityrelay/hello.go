package realityrelay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
)

const maxHelloSize = 64 << 10
const maxFlightSize = 128 << 10

var errHello = errors.New("无效或不受支持的 TLS ClientHello")
var errAuthentication = errors.New("REALITY 首包认证未通过")

type clientHello struct {
	raw           []byte
	random        []byte
	sessionOffset int
	peerKey       []byte
	serverName    string
}

// readHello 按 TLS 记录重组握手；保留每个原始字节，认证仅使用另行复制的握手副本。
func readHello(r io.Reader) ([]byte, *clientHello, error) {
	var flight, handshake []byte
	for len(flight) < maxFlightSize {
		var header [5]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return flight, nil, err
		}
		n := int(binary.BigEndian.Uint16(header[3:]))
		if header[0] != 22 || header[1] != 3 || header[2] > 3 || n == 0 || n > (1<<14)+2048 || len(flight)+5+n > maxFlightSize {
			return flight, nil, errHello
		}
		start := len(flight)
		flight = append(flight, header[:]...)
		flight = append(flight, make([]byte, n)...)
		if _, err := io.ReadFull(r, flight[start+5:]); err != nil {
			return flight, nil, err
		}
		handshake = append(handshake, flight[start+5:]...)
		if len(handshake) < 4 {
			continue
		}
		size := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
		if handshake[0] != 1 || size > maxHelloSize || size < 34 {
			return flight, nil, errHello
		}
		if len(handshake) < size+4 {
			continue
		}
		h, err := parseHello(handshake[:size+4])
		return flight, h, err
	}
	return flight, nil, errHello
}

type helloCursor struct {
	b   []byte
	pos int
}

func (c *helloCursor) take(n int) ([]byte, bool) {
	if n < 0 || n > len(c.b)-c.pos {
		return nil, false
	}
	b := c.b[c.pos : c.pos+n]
	c.pos += n
	return b, true
}
func (c *helloCursor) vector(n int) ([]byte, bool) {
	b, ok := c.take(n)
	if !ok {
		return nil, false
	}
	size := 0
	for _, v := range b {
		size = size<<8 | int(v)
	}
	return c.take(size)
}

func parseHello(raw []byte) (*clientHello, error) {
	c := helloCursor{b: raw, pos: 4}
	version, ok := c.take(2)
	if !ok || binary.BigEndian.Uint16(version) != 0x0303 {
		return nil, errHello
	}
	random, ok := c.take(32)
	if !ok {
		return nil, errHello
	}
	session, ok := c.vector(1)
	if !ok || len(session) != 32 {
		return nil, errHello
	}
	h := &clientHello{raw: raw, random: random, sessionOffset: c.pos - 32}
	suites, ok := c.vector(2)
	if !ok || len(suites) == 0 || len(suites)%2 != 0 {
		return nil, errHello
	}
	compression, ok := c.vector(1)
	if !ok || len(compression) != 1 || compression[0] != 0 {
		return nil, errHello
	}
	extensions, ok := c.vector(2)
	if !ok || c.pos != len(raw) {
		return nil, errHello
	}
	e := helloCursor{b: extensions}
	seen := make(map[uint16]bool)
	var classical, hybrid []byte
	tls13 := false
	for e.pos < len(e.b) {
		idBytes, ok := e.take(2)
		if !ok {
			return nil, errHello
		}
		id := binary.BigEndian.Uint16(idBytes)
		value, ok := e.vector(2)
		if !ok || seen[id] {
			return nil, errHello
		}
		seen[id] = true
		switch id {
		case 0:
			s := helloCursor{b: value}
			list, ok := s.vector(2)
			if !ok || s.pos != len(value) {
				return nil, errHello
			}
			s = helloCursor{b: list}
			for s.pos < len(s.b) {
				kind, ok := s.take(1)
				if !ok {
					return nil, errHello
				}
				name, ok := s.vector(2)
				if !ok {
					return nil, errHello
				}
				if kind[0] == 0 {
					if h.serverName != "" {
						return nil, errHello
					}
					h.serverName = strings.ToLower(string(name))
				}
			}
		case 43:
			s := helloCursor{b: value}
			list, ok := s.vector(1)
			if !ok || s.pos != len(value) || len(list)%2 != 0 {
				return nil, errHello
			}
			for i := 0; i < len(list); i += 2 {
				if binary.BigEndian.Uint16(list[i:]) == 0x0304 {
					tls13 = true
				}
			}
		case 51:
			s := helloCursor{b: value}
			list, ok := s.vector(2)
			if !ok || s.pos != len(value) {
				return nil, errHello
			}
			s = helloCursor{b: list}
			for s.pos < len(s.b) {
				groupBytes, ok := s.take(2)
				if !ok {
					return nil, errHello
				}
				key, ok := s.vector(2)
				if !ok {
					return nil, errHello
				}
				switch binary.BigEndian.Uint16(groupBytes) {
				case 29:
					if classical != nil || len(key) != 32 {
						return nil, errHello
					}
					classical = key
				case 4588:
					if hybrid != nil || len(key) != mlkem.EncapsulationKeySize768+32 {
						return nil, errHello
					}
					hybrid = key[mlkem.EncapsulationKeySize768:]
				}
			}
		}
	}
	if !tls13 || !validServerName(h.serverName) {
		return nil, errHello
	}
	h.peerKey = classical
	if h.peerKey == nil {
		h.peerKey = hybrid
	}
	if h.peerKey == nil {
		return nil, errHello
	}
	return h, nil
}

func (c *compiledConfig) identify(h *clientHello, now time.Time) (*route, error) {
	if !c.names[h.serverName] {
		return nil, errAuthentication
	}
	peer, err := ecdh.X25519().NewPublicKey(h.peerKey)
	if err != nil {
		return nil, errAuthentication
	}
	aad := append([]byte(nil), h.raw...)
	clear(aad[h.sessionOffset : h.sessionOffset+32])
	ciphertext := h.raw[h.sessionOffset : h.sessionOffset+32]
	for _, key := range c.keys {
		shared, err := key.private.ECDH(peer)
		if err != nil {
			continue
		}
		auth, err := hkdf.Key(sha256.New, shared, h.random[:20], "REALITY", 32)
		clear(shared)
		if err != nil {
			continue
		}
		block, err := aes.NewCipher(auth)
		clear(auth)
		if err != nil {
			continue
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			continue
		}
		plain, err := aead.Open(nil, h.random[20:], ciphertext, aad)
		if err != nil || len(plain) != 16 {
			continue
		}
		delta := now.Sub(time.Unix(int64(binary.BigEndian.Uint32(plain[4:8])), 0))
		var sid [8]byte
		copy(sid[:], plain[8:])
		clear(plain)
		if delta > c.MaxTimeDiff || delta < -c.MaxTimeDiff {
			return nil, errAuthentication
		}
		if r := key.routes[sid]; r != nil {
			return r, nil
		}
		return nil, errAuthentication
	}
	return nil, errAuthentication
}
