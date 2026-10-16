package directwg

import "github.com/P0me1oo/YZ-Agent/internal/accesslog"

// 普通 WireGuard 在 Node 的准入层取得用户身份；其他协议由核心适配层采集。
func (m *Managed) SetAccessLog(recorder *accesslog.Recorder) {
	m.state.accessLog = recorder
	if consumer, ok := m.Kernel.(interface{ SetAccessLog(*accesslog.Recorder) }); ok {
		consumer.SetAccessLog(recorder)
	}
}
