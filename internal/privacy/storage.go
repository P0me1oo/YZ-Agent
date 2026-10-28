package privacy

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const Magic = "YZENC1\n"

type Key struct {
	ID    string `json:"id"`
	Value string `json:"key"`
}

type State struct {
	current string
	keys    map[string]cipher.AEAD
}

type envelope struct {
	Version   int             `json:"version"`
	KeyID     string          `json:"key_id"`
	Bootstrap json.RawMessage `json:"bootstrap,omitempty"`
	Data      string          `json:"data"`
}

type binding struct {
	owner     string
	state     *State
	bootstrap json.RawMessage
	recursive bool
}

var registry = struct {
	sync.RWMutex
	paths map[string]binding
	keys  map[string]*State
}{paths: map[string]binding{}, keys: map[string]*State{}}

var writeMu sync.Mutex

func LockWrite() func() { writeMu.Lock(); return writeMu.Unlock }

func New(keys []Key) (*State, error) {
	if len(keys) == 0 || len(keys) > 16 {
		return nil, errors.New("文件保护密钥数量无效")
	}
	s := &State{keys: map[string]cipher.AEAD{}}
	for _, key := range keys {
		raw, err := base64.StdEncoding.DecodeString(key.Value)
		if err != nil || len(raw) != 32 {
			return nil, errors.New("文件保护密钥格式无效")
		}
		sum := sha256.Sum256(raw)
		if key.ID != hex.EncodeToString(sum[:16]) {
			return nil, errors.New("文件保护密钥标识不匹配")
		}
		block, err := aes.NewCipher(raw)
		clear(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, err
		}
		if s.current == "" {
			s.current = key.ID
		}
		s.keys[key.ID] = aead
	}
	registry.Lock()
	for id := range s.keys {
		registry.keys[id] = s
	}
	registry.Unlock()
	return s, nil
}

func IsEncrypted(data []byte) bool { return bytes.HasPrefix(data, []byte(Magic)) }

func StateKey(s *State) string { return s.current }
func CurrentKey(data string) string {
	e, err := parse([]byte(data))
	if err != nil {
		return ""
	}
	return e.KeyID
}

func parse(data []byte) (*envelope, error) {
	if !IsEncrypted(data) {
		return nil, errors.New("不是加密文件")
	}
	var e envelope
	if err := json.Unmarshal(data[len(Magic):], &e); err != nil || e.Version != 1 || len(e.KeyID) != 32 {
		return nil, errors.New("加密文件头无效")
	}
	return &e, nil
}

func BootstrapData(data []byte) (json.RawMessage, error) {
	e, err := parse(data)
	if err != nil {
		return nil, err
	}
	return e.Bootstrap, nil
}

func aad(e *envelope) []byte { return []byte(Magic + e.KeyID + "\n" + string(e.Bootstrap)) }

func (s *State) Seal(data []byte, bootstrap json.RawMessage) ([]byte, error) {
	if len(bootstrap) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, bootstrap); err != nil {
			return nil, err
		}
		bootstrap = compact.Bytes()
	}
	e := &envelope{Version: 1, KeyID: s.current, Bootstrap: bootstrap}
	e.Data = base64.StdEncoding.EncodeToString(s.keys[s.current].Seal(nil, nil, data, aad(e)))
	body, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append([]byte(Magic), body...), nil
}

func Open(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return data, nil
	}
	e, err := parse(data)
	if err != nil {
		return nil, err
	}
	registry.RLock()
	s := registry.keys[e.KeyID]
	registry.RUnlock()
	if s == nil {
		return nil, errors.New("解密密钥尚未从面板取得，保留原文件")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil {
		return nil, errors.New("加密文件内容无效")
	}
	plain, err := s.keys[e.KeyID].Open(nil, nil, ciphertext, aad(e))
	if err != nil {
		return nil, errors.New("加密文件校验失败，保留原文件")
	}
	return plain, nil
}

func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// 只为明确属于本程序的数据路径登记保护规则。
func Bind(path string, s *State, bootstrap json.RawMessage, recursive bool) {
	BindOwner("", path, s, bootstrap, recursive)
}

func BindOwner(owner, path string, s *State, bootstrap json.RawMessage, recursive bool) error {
	release := LockWrite()
	defer release()
	registry.Lock()
	defer registry.Unlock()
	if s == nil {
		delete(registry.paths, canonical(path))
		return nil
	}
	for existing, old := range registry.paths {
		candidate := canonical(path)
		if existing != candidate && !strings.HasPrefix(existing, candidate+string(filepath.Separator)) && !strings.HasPrefix(candidate, existing+string(filepath.Separator)) {
			continue
		}
		common := false
		for id := range old.state.keys {
			if s.keys[id] != nil {
				common = true
			}
		}
		if !common {
			return errors.New("不同机器不能共用同一个加密数据目录")
		}
	}
	if owner != "" {
		owner = canonical(owner)
	}
	registry.paths[canonical(path)] = binding{owner: owner, state: s, bootstrap: bootstrap, recursive: recursive}
	return nil
}

func ResetOwner(owner string) {
	release := LockWrite()
	defer release()
	registry.Lock()
	defer registry.Unlock()
	if owner != "" {
		owner = canonical(owner)
	}
	for path, b := range registry.paths {
		if b.owner == owner {
			delete(registry.paths, path)
		}
	}
}

func CachedState(data string) *State {
	e, err := parse([]byte(data))
	if err != nil {
		return nil
	}
	registry.RLock()
	defer registry.RUnlock()
	return registry.keys[e.KeyID]
}

func lookup(path string) (binding, bool) {
	path = canonical(path)
	registry.RLock()
	defer registry.RUnlock()
	best, length := binding{}, -1
	for prefix, b := range registry.paths {
		if (path == prefix || b.recursive && strings.HasPrefix(path, prefix+string(filepath.Separator))) && len(prefix) > length {
			best, length = b, len(prefix)
		}
	}
	return best, length >= 0
}

func Protect(path string, data []byte) ([]byte, error) {
	if IsEncrypted(data) {
		return data, nil
	}
	if b, ok := lookup(path); ok {
		// 只豁免已登记的主配置及其同目录启动文件，其他目录的同名文件仍需保护。
		if b.owner != "" {
			target, dir := canonical(path), filepath.Dir(b.owner)
			if target == b.owner || target == filepath.Join(dir, "credentials.env") || target == filepath.Join(dir, "install-meta.json") {
				return data, nil
			}
		}
		if old, err := os.ReadFile(path); err == nil && IsEncrypted(old) {
			e, err := parse(old)
			if err != nil {
				return nil, err
			}
			if b.state.keys[e.KeyID] == nil {
				return nil, errors.New("不能用其他机器的密钥覆盖加密文件")
			}
		}
		return b.state.Seal(data, b.bootstrap)
	}
	// 未确认所属身份时不能覆盖已有加密文件。
	old, err := os.ReadFile(path)
	if err == nil && IsEncrypted(old) {
		return nil, errors.New("加密文件未关联当前机器，保留原文件")
	}
	return data, nil
}

func OpenFor(path string, data []byte) ([]byte, error) {
	if IsEncrypted(data) {
		e, err := parse(data)
		if err != nil {
			return nil, err
		}
		b, ok := lookup(path)
		if !ok || b.state.keys[e.KeyID] == nil {
			return nil, errors.New("加密文件不属于当前已认证的机器，保留原文件")
		}
	}
	return Open(data)
}

func ReadFile(path string) ([]byte, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > 96<<20 {
		return nil, errors.New("加密数据文件过大")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return OpenFor(path, data)
}

func WriteFile(path string, data []byte, perm os.FileMode) error {
	release := LockWrite()
	defer release()
	data, err := Protect(path, data)
	if err != nil {
		return err
	}
	return WriteRaw(path, data, perm)
}

// 同目录临时文件先完整写入并同步，失败时不替换旧快照。
func WriteRaw(path string, data []byte, perm os.FileMode) error {
	if IsEncrypted(data) {
		perm = 0o600
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".private-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(perm); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func MigrateFile(path string, s *State) error {
	release := LockWrite()
	defer release()
	if info, err := os.Stat(path); err == nil && info.Size() > 96<<20 {
		return errors.New("待保护文件过大，保留原文件")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if IsEncrypted(data) {
		e, err := parse(data)
		if err != nil {
			return err
		}
		if s.keys[e.KeyID] == nil {
			return errors.New("加密文件不属于当前机器，保留原文件")
		}
		data, err = Open(data)
		if err != nil {
			return err
		}
		if e.KeyID == s.current {
			return nil
		}
	}
	if len(data) > 64<<20 {
		return fmt.Errorf("待保护文件过大，保留原文件: %s", filepath.Base(path))
	}
	sealed, err := Protect(path, data)
	if err != nil {
		return err
	}
	return WriteRaw(path, sealed, 0o600)
}
