package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

// pendingReportStore 把尚未确认的流量写入节点自己的配置目录，进程重启后继续上报。
//
// 每批报告在发送前先写入磁盘（连同批次编号），面板确认后再移除。重启后原样重发的
// 批次沿用原编号，在面板去重记录保留期内避免重复计费。尚未组成批次的
// 累计流量随同保存，它们还没发出过，恢复后并入下一批即可。
//
// 文件名包含面板地址和节点编号的摘要：配置目录被改绑到其他节点时，旧文件不会
// 被误报给新节点。设备、在线人数和超限事件是即时状态，不保存。
type pendingReportStore struct {
	path     string
	identity string
	mu       sync.Mutex
}

const pendingReportVersion = 1

type pendingTraffic struct {
	Traffic   map[int][2]int64         `json:"traffic,omitempty"`
	Relay     map[int][2]int64         `json:"relay,omitempty"`
	RelayUser map[int]map[int][2]int64 `json:"relay_user,omitempty"`
}

func (p pendingTraffic) empty() bool {
	return len(p.Traffic) == 0 && len(p.Relay) == 0 && len(p.RelayUser) == 0
}

type pendingBatch struct {
	ID string `json:"id"`
	pendingTraffic
}

type pendingReportFile struct {
	Version  int            `json:"version"`
	Identity string         `json:"identity"`
	SavedAt  time.Time      `json:"saved_at"`
	Batch    *pendingBatch  `json:"batch,omitempty"`
	Pending  pendingTraffic `json:"pending"`
}

// newPendingReportStore 为向面板上报的节点创建存储；独立运行或没有配置目录时返回 nil。
func newPendingReportStore(cfg *config.Config) *pendingReportStore {
	if cfg == nil || cfg.IsStandalone() || strings.TrimSpace(cfg.Kernel.ConfigDir) == "" {
		return nil
	}
	identity := fmt.Sprintf("%s|machine=%d|node=%d",
		strings.TrimRight(strings.TrimSpace(cfg.Panel.URL), "/"), cfg.Panel.MachineID, cfg.Panel.NodeID)
	sum := sha256.Sum256([]byte(identity))
	name := "pending-report-" + hex.EncodeToString(sum[:6]) + ".json"
	return &pendingReportStore{path: filepath.Join(cfg.Kernel.ConfigDir, name), identity: identity}
}

// save 用临时文件替换的方式写入，写到一半断电也不会留下损坏的文件。
// 没有任何待确认数据时删除文件。
func (s *pendingReportStore) save(batch *pendingBatch, pending pendingTraffic) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if (batch == nil || batch.empty()) && pending.empty() {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		syncDir(filepath.Dir(s.path))
		return nil
	}
	if batch != nil && batch.empty() {
		batch = nil
	}
	data, err := json.Marshal(pendingReportFile{
		Version:  pendingReportVersion,
		Identity: s.identity,
		SavedAt:  time.Now().UTC(),
		Batch:    batch,
		Pending:  pending,
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil && !isChmodUnsupported(err) {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return err
	}
	syncDir(dir)
	return nil
}

// load 读取上次保存的数据。文件不存在时返回空结果；内容损坏或属于其他节点时
// 把文件改名保留，交由管理员核对，本次按空结果继续运行。
func (s *pendingReportStore) load() (*pendingBatch, pendingTraffic, error) {
	if s == nil {
		return nil, pendingTraffic{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, pendingTraffic{}, nil
	}
	if err != nil {
		return nil, pendingTraffic{}, err
	}
	var file pendingReportFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != pendingReportVersion || file.Identity != s.identity {
		aside := fmt.Sprintf("%s.invalid-%d", s.path, time.Now().Unix())
		if renameErr := os.Rename(s.path, aside); renameErr != nil {
			return nil, pendingTraffic{}, fmt.Errorf("pending report file unreadable and could not be moved aside: %w", renameErr)
		}
		if err == nil {
			err = fmt.Errorf("version or node identity mismatch")
		}
		return nil, pendingTraffic{}, fmt.Errorf("pending report file moved to %s: %w", aside, err)
	}
	if file.Batch != nil && (file.Batch.ID == "" || file.Batch.empty()) {
		file.Batch = nil
	}
	return file.Batch, file.Pending, nil
}

func isChmodUnsupported(err error) bool {
	// Windows 等平台不支持完整的权限位，只影响本地开发。
	return errors.Is(err, errors.ErrUnsupported)
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	// 部分平台不支持同步目录，失败不影响已完成的替换。
	_ = f.Sync()
	_ = f.Close()
}
