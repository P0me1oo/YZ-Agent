package balancer

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// writeProxyHeader 使用标准 PROXY v2，真实来源在 Reality 握手前交给已有核心。
func writeProxyHeader(conn net.Conn, source, destination net.Addr) error {
	src, srcOK := source.(*net.TCPAddr)
	dst, dstOK := destination.(*net.TCPAddr)
	if !srcOK || !dstOK {
		return errors.New("TCP 来源地址无效")
	}
	header := []byte{13, 10, 13, 10, 0, 13, 10, 81, 85, 73, 84, 10, 0x21, 0x11, 0, 12}
	if src.IP.To4() != nil && dst.IP.To4() != nil {
		header = append(header, src.IP.To4()...)
		header = append(header, dst.IP.To4()...)
	} else {
		if src.IP.To16() == nil || dst.IP.To16() == nil {
			return errors.New("TCP 来源地址无效")
		}
		header[13], header[15] = 0x21, 36
		header = append(header, src.IP.To16()...)
		header = append(header, dst.IP.To16()...)
	}
	header = binary.BigEndian.AppendUint16(header, uint16(src.Port))
	header = binary.BigEndian.AppendUint16(header, uint16(dst.Port))
	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Write(header)
	conn.SetWriteDeadline(time.Time{})
	if err == nil && n != len(header) {
		err = io.ErrShortWrite
	}
	return err
}
