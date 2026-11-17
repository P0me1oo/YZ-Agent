package privacy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// 每条记录独立认证加密，文件只保存格式、长度和密钥编号。
const LogMagic = "YZLOG1\n"
const maxLogRecord = 1 << 20

type LogWriter struct {
	mu            sync.Mutex
	path          string
	state         *State
	maxBytes      int64
	keep          int
	validatedSize int64
}

func NewLogWriter(path string, state *State) *LogWriter {
	return &LogWriter{path: path, state: state, maxBytes: 20 << 20, keep: 3, validatedSize: -1}
}

func (w *LogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == nil || len(data) > maxLogRecord/2 {
		return 0, errors.New("加密日志密钥或记录无效")
	}
	sealed, err := w.state.Seal(data, nil)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("加密日志必须为普通文件")
	}
	if info.Size() > 96<<20 {
		return 0, errors.New("加密日志文件过大，保留原文件")
	}
	if err := f.Chmod(0o600); err != nil {
		return 0, err
	}
	if info.Size() > 0 && info.Size() != w.validatedSize {
		// 重启或外部修改后校验旧记录；损坏、缺少历史密钥时保全原文件。
		if err := w.state.ReadLog(io.Discard, f); err != nil {
			return 0, err
		}
	}
	if info.Size() > 0 && info.Size()+int64(len(sealed)+4) > w.maxBytes {
		if err := f.Close(); err != nil {
			return 0, err
		}
		if err := w.rotate(); err != nil {
			return 0, err
		}
		f, err = os.OpenFile(w.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		info, err = f.Stat()
		if err != nil {
			return 0, err
		}
	}
	frame := make([]byte, 0, len(LogMagic)+4+len(sealed))
	if info.Size() == 0 {
		frame = append(frame, LogMagic...)
	}
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(sealed)))
	frame = append(frame, sealed...)
	n, err := f.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.validatedSize = -1
		return 0, err
	}
	w.validatedSize = info.Size() + int64(n)
	return len(data), nil
}

func (w *LogWriter) rotate() error {
	if err := os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := w.keep - 1; i >= 1; i-- {
		if err := os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	w.validatedSize = -1
	return os.Rename(w.path, w.path+".1")
}

// ReadLog 用当前机器的内存密钥恢复记录，不创建明文文件。
// 尾部损坏时返回错误，此前完整记录仍可读取。
func (s *State) ReadLog(out io.Writer, in io.Reader) error {
	header := make([]byte, len(LogMagic))
	if _, err := io.ReadFull(in, header); err != nil {
		return err
	}
	if string(header) != LogMagic {
		return errors.New("加密日志格式无效")
	}
	for {
		var size [4]byte
		_, err := io.ReadFull(in, size[:])
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		length := binary.BigEndian.Uint32(size[:])
		if length == 0 || length > maxLogRecord {
			return errors.New("加密日志记录长度无效")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(in, data); err != nil {
			return err
		}
		if !IsEncrypted(data) {
			return errors.New("加密日志包含无效记录")
		}
		plain, err := s.Open(data)
		if err != nil {
			return err
		}
		n, writeErr := out.Write(plain)
		if writeErr == nil && n != len(plain) {
			writeErr = io.ErrShortWrite
		}
		clear(plain)
		if writeErr != nil {
			return writeErr
		}
	}
}
