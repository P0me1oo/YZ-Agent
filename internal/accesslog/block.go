package accesslog

import "context"

type userContextKey struct{}

// 普通 WireGuard 的用户身份来自 Node 准入层，不写入核心认证字段。
func ContextWithUser(ctx context.Context, user int) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

func ContextUser(ctx context.Context) int {
	user, _ := ctx.Value(userContextKey{}).(int)
	return user
}

func (r *Recorder) AddBlock(user, rule int, domain, address string, port uint16, network string) {
	if rule <= 0 || user < 0 || (network != "tcp" && network != "udp") {
		return
	}
	r.add(0, user, domain, address, port, rule, network)
}
