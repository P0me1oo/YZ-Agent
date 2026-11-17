package healthcheck

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sync"
)

// Prober 由一份节点配置持有，最多记录该配置下十七条出站的成功地址。
// 只调整尝试顺序；每轮仍使用新解析且通过公网校验的全部地址。
type Prober struct {
	mu        sync.Mutex
	preferred map[string]string
}

func (p *Prober) Request(
	ctx context.Context,
	route string,
	u *url.URL,
	addresses []string,
	dial DialFunc,
) (int64, error) {
	return p.request(
		ctx,
		route,
		u,
		addresses,
		requestTransport(u, dial),
	)
}

func (p *Prober) request(
	ctx context.Context,
	route string,
	u *url.URL,
	addresses []string,
	transport *http.Transport,
) (int64, error) {
	p.mu.Lock()
	preferred := p.preferred[route]
	p.mu.Unlock()
	if index := slices.Index(addresses, preferred); index > 0 {
		ordered := slices.Clone(addresses)
		copy(ordered[1:index+1], ordered[:index])
		ordered[0] = preferred
		addresses = ordered
	}
	ms, successful, err := requestWithAddress(
		ctx,
		u,
		addresses,
		transport,
	)
	if err == nil && ctx.Err() == nil {
		p.mu.Lock()
		if p.preferred == nil {
			p.preferred = make(map[string]string)
		}
		p.preferred[route] = successful
		p.mu.Unlock()
	}
	return ms, err
}
