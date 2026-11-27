//go:build with_wireguard

package healthcheck

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"strconv"

	wg "github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

type probeWGSocket struct{}

func (probeWGSocket) UDPListenerControl() (control.Func, bool) { return nil, false }
func (probeWGSocket) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, dest.String())
}
func (probeWGSocket) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", ":0")
}

func wireGuardClient(ctx context.Context, raw any) (DialFunc, func(), error) {
	var cfg struct {
		Private string         `json:"private_key"`
		Address []netip.Prefix `json:"address"`
		MTU     uint32         `json:"mtu"`
		Peers   []struct {
			Address   string         `json:"address"`
			Port      int            `json:"port"`
			Public    string         `json:"public_key"`
			IPs       []netip.Prefix `json:"allowed_ips"`
			Keepalive uint16         `json:"persistent_keepalive_interval"`
		} `json:"peers"`
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		return nil, nil, err
	}
	peers := []wg.PeerOptions{}
	for _, p := range cfg.Peers {
		host := p.Address
		if net.ParseIP(host) == nil {
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, nil, err
			}
			if len(ips) > 0 {
				host = ips[0].String()
			}
		}
		peers = append(peers, wg.PeerOptions{Endpoint: M.ParseSocksaddr(net.JoinHostPort(host, strconv.Itoa(p.Port))), PublicKey: p.Public, AllowedIPs: p.IPs, PersistentKeepaliveInterval: p.Keepalive})
	}
	endpoint, err := wg.NewEndpoint(wg.EndpointOptions{Context: ctx, Logger: logger.NOP(), Dialer: probeWGSocket{}, PrivateKey: cfg.Private, Address: cfg.Address, MTU: cfg.MTU, Peers: peers})
	if err != nil {
		return nil, nil, err
	}
	if err := endpoint.Start(false); err != nil {
		endpoint.Close()
		return nil, nil, err
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return endpoint.DialContext(ctx, network, M.ParseSocksaddr(address))
	}, func() { endpoint.Close() }, nil
}
