package sourcepolicy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go4.org/netipx"
)

const (
	chinaURL        = "https://raw.githubusercontent.com/Loyalsoldier/geoip/release/text/cn.txt"
	maxBytes        = 4 << 20
	refreshInterval = 24 * time.Hour
	retryInterval   = 5 * time.Minute
)

// Snapshot 的地址集合不可修改，可在多个节点之间安全共享。
type Snapshot struct {
	Revision string
	set      *netipx.IPSet
}

func (s Snapshot) Ready() bool { return s.set != nil }

type Store struct {
	mu        sync.Mutex
	dir       string
	url       string
	client    *http.Client
	current   Snapshot
	nextCheck time.Time
	lastErr   error
}

var stores sync.Map

// ForDir 复用同一目录的数据与下载锁，避免多节点同时下载和替换文件。
func ForDir(dir string) *Store {
	dir, _ = filepath.Abs(dir)
	store, _ := stores.LoadOrStore(dir, &Store{
		dir: dir, url: chinaURL,
		client: &http.Client{Timeout: 20 * time.Second},
	})
	return store.(*Store)
}

// Get 首次失败不提供空规则；更新失败返回上一份有效数据及错误。
func (s *Store) Get(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Before(s.nextCheck) {
		return s.current, s.lastErr
	}
	file := filepath.Join(s.dir, "cn-source.txt")
	if !s.current.Ready() {
		if data, err := readLimitedFile(file); err == nil {
			if snapshot, err := Parse(data); err == nil {
				s.current = snapshot
				if info, err := os.Stat(file); err == nil && now.Sub(info.ModTime()) < refreshInterval {
					s.nextCheck = info.ModTime().Add(refreshInterval)
					return s.current, nil
				}
			}
		}
	}
	data, err := s.download(ctx)
	var candidate Snapshot
	if err == nil {
		candidate, err = Parse(data)
	}
	if err == nil {
		err = writeAtomic(file, data)
	}
	if err != nil {
		s.lastErr = fmt.Errorf("更新大陆来源网段库失败: %w", err)
		s.nextCheck = now.Add(retryInterval)
		return s.current, s.lastErr
	}
	s.current, s.lastErr = candidate, nil
	s.nextCheck = now.Add(refreshInterval)
	return s.current, nil
}

func (s *Store) download(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载返回 HTTP %d", response.StatusCode)
	}
	return readLimited(response.Body)
}

func readLimitedFile(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f)
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err == nil && len(data) > maxBytes {
		err = fmt.Errorf("网段库超过大小限制")
	}
	return data, err
}

// Parse 同时要求 IPv4、IPv6，拒绝空数据、错误页面和全地址网段。
func Parse(data []byte) (Snapshot, error) {
	var builder netipx.IPSetBuilder
	var v4, v6 bool
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix, err := netip.ParsePrefix(line)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return Snapshot{}, fmt.Errorf("网段库包含无效网段")
		}
		if prefix.Addr().Is4() {
			v4 = true
		} else {
			v6 = true
		}
		builder.AddPrefix(prefix.Masked())
	}
	if err := scanner.Err(); err != nil {
		return Snapshot{}, err
	}
	if !v4 || !v6 {
		return Snapshot{}, fmt.Errorf("网段库必须同时包含 IPv4 和 IPv6")
	}
	set, err := builder.IPSet()
	if err != nil {
		return Snapshot{}, err
	}
	// 使用规范化集合计算版本，空白、顺序或重复条目变化不触发内核重载。
	h := sha256.New()
	for _, prefix := range set.Prefixes() {
		fmt.Fprintln(h, prefix)
	}
	return Snapshot{Revision: fmt.Sprintf("%x", h.Sum(nil)), set: set}, nil
}

func writeAtomic(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".cn-source-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
