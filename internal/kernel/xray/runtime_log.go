package xray

import (
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	applog "github.com/xtls/xray-core/app/log"
	clog "github.com/xtls/xray-core/common/log"
)

type runtimeLogHandler struct{ inner clog.Handler }

func (h *runtimeLogHandler) Close() error {
	if closer, ok := h.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (h *runtimeLogHandler) Handle(message clog.Message) {
	h.inner.Handle(message)
	if general, ok := message.(*clog.GeneralMessage); ok {
		level := "debug"
		switch general.Severity {
		case clog.Severity_Error:
			level = "error"
		case clog.Severity_Warning:
			level = "warn"
		case clog.Severity_Info:
			level = "info"
		}
		// 核心处理器为进程共享，没有可信节点上下文，禁止推测节点归属。
		nlog.Publish("", nlog.Event{Source: "xray", Level: level, Message: message.String()})
	}
}
func init() {
	_ = applog.RegisterHandlerCreator(applog.LogType_Console, func(_ applog.LogType, _ applog.HandlerCreatorOptions) (clog.Handler, error) {
		return &runtimeLogHandler{inner: clog.NewLogger(clog.CreateStdoutLogWriter())}, nil
	})
}
