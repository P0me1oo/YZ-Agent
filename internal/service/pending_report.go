package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

// pendingReportStore 把尚未确认的流量写入节点自己的配置目录，进程重启后继续上报。
//
// 每批报告在发送前先写入磁盘（连同批次编号），面板确认后再移除。重启后原样重发的
// 批次沿用原编号，在面板去重记录保留期内避免重复计费。尚未组成批次的
// 累计流量随同保存，它们还没发出过，恢复后并入下一批即可。
//
// 文件名包含面板地址和节点编号的摘要：配置目录被改绑到其他节点时，旧文件不会
// 被误报给新节点。设备和在线人数是即时状态，不保存；已组成批次的限制事件保存并继续重试。
type pendingReportStore struct {
	path       string
	identity   string
	mu         sync.Mutex
	lastHash   [32]byte
	saved      bool
	lastState  pendingReportFile
	journalSeq uint64
	loadErr    error
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
	ID          string             `json:"id"`
	LimitEvents []panel.LimitEvent `json:"limit_events,omitempty"`
	pendingTraffic
}

func (p pendingBatch) empty() bool { return p.pendingTraffic.empty() && len(p.LimitEvents) == 0 }

type pendingReportFile struct {
	Version         int            `json:"version"`
	JournalSequence uint64         `json:"journal_sequence,omitempty"`
	Identity        string         `json:"identity"`
	SavedAt         time.Time      `json:"saved_at"`
	Batch           *pendingBatch  `json:"batch,omitempty"`
	Pending         pendingTraffic `json:"pending"`
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
	release := privacy.LockFileWrite(s.path)
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("pending storage recovery failed, preserving files: %w", s.loadErr)
	}
	if batch != nil && batch.empty() {
		batch = nil
	}
	state := pendingReportFile{Version: pendingReportVersion, Identity: s.identity, Batch: batch, Pending: pending}
	hash, err := pendingStateHash(state)
 if err != nil { return err }
	if s.saved && hash == s.lastHash {
		return nil
	}
	empty := batch == nil && pending.empty()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 首次、每 128 次变化和清空时发布检查点；中间只追加变化记录。
	if !s.saved || s.journalSeq%128 == 127 || empty {
		state.JournalSequence = s.journalSeq + 1
		checkpoint, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if err := s.writeProtected(s.path, checkpoint); err != nil {
			return err
		}
		s.journalSeq = state.JournalSequence
		if err := os.RemoveAll(s.path + ".journal"); err != nil {
			return err
		}
		if empty {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			syncDir(dir)
		}
	} else {
		record := pendingJournalRecord{Sequence: s.journalSeq + 1, Pending: diffPending(s.lastState.Pending, pending)}
		before, _ := json.Marshal(s.lastState.Batch)
		after, _ := json.Marshal(batch)
		if string(before) != string(after) {
			record.BatchChanged = true
			record.Batch = batch
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		journal := s.path + ".journal"
		if err := os.MkdirAll(journal, 0o700); err != nil {
			return err
		}
		syncDir(dir)
		if err := s.writeProtected(filepath.Join(journal, fmt.Sprintf("%020d.json", record.Sequence)), encoded); err != nil {
			return err
		}
		s.journalSeq = record.Sequence
	}
	// 输入由采样复制，检查点状态另留独立副本，避免批次移动后改写比较基线。
	s.lastState = clonePendingState(state)
 s.lastState.JournalSequence = 0
	s.lastHash, s.saved = hash, true
	return nil
}

func (s *pendingReportStore) writeProtected(path string, data []byte) error {
	protected, err := privacy.Protect(path, data)
	if err != nil {
		return err
	}
	return privacy.WriteRaw(path, protected, 0o600)
}

// compact 在正常退出前合并增量；历史版本仍能按旧单快照恢复未确认流量。
func (s *pendingReportStore) compact() error {
	if s == nil {
		return nil
	}
	release := privacy.LockFileWrite(s.path)
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	if !s.saved || (s.lastState.Batch == nil && s.lastState.Pending.empty()) {
		return nil
	}
	state := s.lastState
	state.JournalSequence = s.journalSeq
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := s.writeProtected(s.path, data); err != nil {
		return err
	}
	if err := os.RemoveAll(s.path + ".journal"); err != nil {
		return err
	}
	syncDir(filepath.Dir(s.path))
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
	data, err := privacy.ReadFile(s.path)
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
		if renameErr := os.Rename(s.path+".journal", aside+".journal"); renameErr != nil && !os.IsNotExist(renameErr) {
			return nil, pendingTraffic{}, renameErr
		}
		if err == nil {
			err = fmt.Errorf("version or node identity mismatch")
		}
		return nil, pendingTraffic{}, fmt.Errorf("pending report file moved to %s: %w", aside, err)
	}
	if err := s.replayJournal(&file); err != nil {
		s.loadErr = err
		return nil, pendingTraffic{}, err
	}
	s.journalSeq = file.JournalSequence
	file.JournalSequence = 0
	hash, err := pendingStateHash(file)
 if err != nil { return nil, pendingTraffic{}, err }
 s.lastState = clonePendingState(file)
 s.lastHash, s.saved = hash, true
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
