package xray

import (
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	applog "github.com/xtls/xray-core/app/log"
	clog "github.com/xtls/xray-core/common/log"
)

type runtimeLogHandler struct{}

func (h *runtimeLogHandler) Close() error {
	return nil
}

func (h *runtimeLogHandler) Handle(message clog.Message) {
	// 直接经过受策略锁保护的出口，避免核心异步队列在切换后补写明文。
	nlog.WriteRuntime(time.Now().Format(time.RFC3339Nano) + " " + message.String())
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
		return &runtimeLogHandler{}, nil
	})
	// 核心实例创建前的初始化日志也使用同一出口。
	clog.RegisterHandler(&runtimeLogHandler{})
}
