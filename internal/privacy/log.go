package privacy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	file          *os.File
	info          os.FileInfo
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
	info, err := w.prepare()
	if err != nil {
		_ = w.closeFile()
		return 0, err
	}
	if info.Size() > 0 && info.Size()+int64(len(sealed)+4) > w.maxBytes {
		if err := w.closeFile(); err != nil {
			return 0, err
		}
		if err := w.rotate(); err != nil {
			return 0, err
		}
		info, err = w.prepare()
		if err != nil {
			_ = w.closeFile()
			return 0, err
		}
	}
	frame := make([]byte, 0, len(LogMagic)+4+len(sealed))
	if info.Size() == 0 {
		frame = append(frame, LogMagic...)
	}
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(sealed)))
	frame = append(frame, sealed...)
	n, err := w.file.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = w.closeFile()
		return 0, err
	}
	w.validatedSize = info.Size() + int64(n)
	w.info, err = w.file.Stat()
	if err != nil {
		_ = w.closeFile()
		return 0, err
	}
	return len(data), nil
}

// 复用句柄，但每次追加仍核对路径身份、长度及修改时间，不能继续写入被移走的旧文件。
func (w *LogWriter) prepare() (os.FileInfo, error) {
	info, err := os.Lstat(w.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, errors.New("加密日志必须为普通文件")
	}
	if w.file != nil && (info == nil || !os.SameFile(w.info, info)) {
		if err := w.closeFile(); err != nil {
			return nil, err
		}
	}
	if w.file == nil {
		if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
			return nil, err
		}
		w.file, err = os.OpenFile(w.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		info, err = w.file.Stat()
		if err != nil {
			return nil, err
		}
	}
	if !info.Mode().IsRegular() || info.Size() > 96<<20 {
		return nil, errors.New("加密日志类型或大小无效，保留原文件")
	}
	if info.Mode() != 0o600 {
		if err := w.file.Chmod(0o600); err != nil {
			return nil, err
		}
	}
	unchanged := w.info != nil && info.Size() == w.validatedSize &&
		info.ModTime().Equal(w.info.ModTime())
	if info.Size() > 0 && !unchanged {
		// 独立读取位置，支持复用句柄后的重新校验；同长度的外部改写也必须校验。
		if err := w.state.ReadLog(io.Discard, io.NewSectionReader(w.file, 0, info.Size())); err != nil {
			if errors.Is(err, errIncompleteLog) {
				if closeErr := w.closeFile(); closeErr != nil {
					return nil, closeErr
				}
				backup := fmt.Sprintf("%s.partial-%d", w.path, time.Now().UnixNano())
				if renameErr := os.Rename(w.path, backup); renameErr != nil {
					return nil, renameErr
				}
				return w.prepare()
			}
			return nil, err
		}
	}
	return info, nil
}

// Close 释放当前句柄；再次开启隐私模式时，下次写入会重新打开并校验原文件。
func (w *LogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeFile()
}

func (w *LogWriter) closeFile() error {
	file := w.file
	w.file, w.info, w.validatedSize = nil, nil, -1
	if file == nil {
		return nil
	}
	return file.Close()
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

var errIncompleteLog = errors.New("加密日志尾部记录不完整")

// ReadLog 用当前机器的内存密钥恢复记录，不创建明文文件。
// 尾部损坏时返回错误，此前完整记录仍可读取。
func (s *State) ReadLog(out io.Writer, in io.Reader) error {
	header := make([]byte, len(LogMagic))
	if n, err := io.ReadFull(in, header); err != nil {
		if n > 0 && errors.Is(err, io.ErrUnexpectedEOF) && bytes.HasPrefix([]byte(LogMagic), header[:n]) {
			return errIncompleteLog
		}
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
		if err == io.ErrUnexpectedEOF {
			return errIncompleteLog
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
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return errIncompleteLog
			}
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
