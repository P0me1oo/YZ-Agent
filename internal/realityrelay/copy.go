package realityrelay

import (
	"io"
	"net"
)

type readerOnly struct{ io.Reader }
type writerOnly struct{ io.Writer }

func copyTCP(destination, source *net.TCPConn, mode string) (int64, error) {
	if mode != "buffered" && spliceSupported() {
		// 必须保留两端原始 TCPConn，避免统计/限速包装器阻断 Go 的 Linux splice 路径。
		// 内核不支持时标准库可能回退；splice 是否实际发生由 Linux 系统调用检查确认。
		return destination.ReadFrom(source)
	}
	// 显式屏蔽 ReaderFrom/WriterTo，使对照组确实使用用户态缓冲复制。
	return io.CopyBuffer(writerOnly{destination}, readerOnly{source}, make([]byte, 32<<10))
}
