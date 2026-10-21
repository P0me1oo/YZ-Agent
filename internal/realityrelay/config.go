// Package realityrelay 提供独立于代理核心的实验性 REALITY 首包选路入口。
package realityrelay

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// 配置只引用环境变量或私钥文件，检查结果和统计不会返回密钥内容。
type Config struct {
	Listen           string        `yaml:"listen" json:"listen"`
	ServerNames      []string      `yaml:"server_names" json:"server_names"`
	Keys             []KeyConfig   `yaml:"keys" json:"keys"`
	Routes           []RouteConfig `yaml:"routes" json:"routes"`
	Fallback         string        `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	CopyMode         string        `yaml:"copy_mode,omitempty" json:"copy_mode,omitempty"`
	HandshakeTimeout time.Duration `yaml:"handshake_timeout,omitempty" json:"handshake_timeout,omitempty"`
	DialTimeout      time.Duration `yaml:"dial_timeout,omitempty" json:"dial_timeout,omitempty"`
	MaxTimeDiff      time.Duration `yaml:"max_time_diff,omitempty" json:"max_time_diff,omitempty"`
	MaxConnections   int           `yaml:"max_connections,omitempty" json:"max_connections,omitempty"`
	MaxHandshakes    int           `yaml:"max_handshakes,omitempty" json:"max_handshakes,omitempty"`
}

type KeyConfig struct {
	Name           string `yaml:"name" json:"name"`
	PrivateKeyEnv  string `yaml:"private_key_env,omitempty" json:"private_key_env,omitempty"`
	PrivateKeyFile string `yaml:"private_key_file,omitempty" json:"private_key_file,omitempty"`
}

type RouteConfig struct {
	Name    string `yaml:"name" json:"name"`
	Key     string `yaml:"key" json:"key"`
	ShortID string `yaml:"short_id" json:"short_id"`
	Target  string `yaml:"target" json:"target"`
}

type route struct {
	name, target string
	stats        *routeCounters
}

type authKey struct {
	private *ecdh.PrivateKey
	routes  map[[8]byte]*route
}

type compiledConfig struct {
	Config
	names       map[string]bool
	keys        []authKey
	routes      []*route
	fallback    *route
	fingerprint [32]byte
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("打开实验中转配置失败: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return Config{}, errors.New("实验中转配置读取失败或超过 1 MiB")
	}
	var cfg Config
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		// 解析器可能引用输入，不把可能误填的密钥带到日志。
		return Config{}, errors.New("实验中转 YAML 格式无效或包含未知字段")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Config{}, errors.New("实验中转配置只能包含一个 YAML 文档")
	}
	return cfg, nil
}

func compileConfig(cfg Config) (*compiledConfig, error) {
	// 调用方随后修改自己的配置副本时，不能改变已生效快照。
	cfg.ServerNames = append([]string(nil), cfg.ServerNames...)
	cfg.Keys = append([]KeyConfig(nil), cfg.Keys...)
	cfg.Routes = append([]RouteConfig(nil), cfg.Routes...)
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 5 * time.Second
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.MaxTimeDiff == 0 {
		cfg.MaxTimeDiff = 2 * time.Minute
	}
	if cfg.MaxConnections == 0 {
		cfg.MaxConnections = 4096
	}
	if cfg.MaxHandshakes == 0 {
		cfg.MaxHandshakes = 256
	}
	if cfg.CopyMode == "" {
		cfg.CopyMode = "auto"
	}
	listenHost, listenPort, listenErr := net.SplitHostPort(cfg.Listen)
	_, portErr := strconv.ParseUint(listenPort, 10, 16)
	if listenErr != nil || portErr != nil || (listenHost != "" && net.ParseIP(listenHost) == nil) {
		return nil, errors.New("listen 必须是带端口的监听地址")
	}
	if cfg.HandshakeTimeout < time.Millisecond || cfg.HandshakeTimeout > time.Minute || cfg.DialTimeout < time.Millisecond || cfg.DialTimeout > time.Minute {
		return nil, errors.New("握手及连接超时必须在 1 毫秒至 1 分钟内")
	}
	if cfg.MaxTimeDiff < time.Second || cfg.MaxTimeDiff > 24*time.Hour {
		return nil, errors.New("认证时间窗口必须在 1 秒至 24 小时内")
	}
	if cfg.MaxConnections < 1 || cfg.MaxConnections > 1_000_000 || cfg.MaxHandshakes < 1 || cfg.MaxHandshakes > cfg.MaxConnections {
		return nil, errors.New("连接数和并行握手上限无效")
	}
	if cfg.CopyMode != "auto" && cfg.CopyMode != "splice" && cfg.CopyMode != "buffered" {
		return nil, errors.New("copy_mode 只能为 auto、splice 或 buffered")
	}
	if cfg.CopyMode == "splice" && !spliceSupported() {
		return nil, errors.New("splice 实验模式只支持 Linux")
	}
	c := &compiledConfig{Config: cfg, names: make(map[string]bool)}
	for _, name := range cfg.ServerNames {
		name = strings.ToLower(name)
		if !validServerName(name) || c.names[name] {
			return nil, errors.New("server_names 包含无效或重复的服务名")
		}
		c.names[name] = true
	}
	if len(c.names) == 0 || len(c.names) > 256 {
		return nil, errors.New("需要配置 1 至 256 个允许的服务名")
	}
	if len(cfg.Keys) == 0 || len(cfg.Keys) > 32 {
		return nil, errors.New("需要配置 1 至 32 组认证密钥")
	}
	if len(cfg.Routes) == 0 || len(cfg.Routes) > 65535 {
		return nil, errors.New("需要配置 1 至 65535 条线路")
	}
	keyIndices := make(map[string]int)
	publicKeys := make(map[string]bool)
	publicIDs := make([]string, 0, len(cfg.Keys))
	for _, k := range cfg.Keys {
		if !validName(k.Name) {
			return nil, errors.New("密钥名称无效")
		}
		if _, ok := keyIndices[k.Name]; ok {
			return nil, errors.New("密钥名称重复")
		}
		private, err := loadPrivateKey(k)
		if err != nil {
			return nil, err
		}
		public := hex.EncodeToString(private.PublicKey().Bytes())
		if publicKeys[public] {
			return nil, errors.New("同一认证密钥不能重复配置")
		}
		publicKeys[public] = true
		publicIDs = append(publicIDs, public)
		keyIndices[k.Name] = len(c.keys)
		c.keys = append(c.keys, authKey{private: private, routes: make(map[[8]byte]*route)})
	}
	routeNames := make(map[string]bool)
	for _, rc := range cfg.Routes {
		if !validName(rc.Name) || rc.Name == "fallback" || routeNames[rc.Name] {
			return nil, errors.New("线路名称无效或重复")
		}
		idx, ok := keyIndices[rc.Key]
		if !ok {
			return nil, errors.New("线路引用了未配置的认证密钥")
		}
		sid, err := parseShortID(rc.ShortID)
		if err != nil {
			return nil, err
		}
		if c.keys[idx].routes[sid] != nil {
			return nil, errors.New("同一认证密钥下的 short_id 不能重复")
		}
		if !validTarget(rc.Target) {
			return nil, errors.New("线路目标必须是有效的主机和非零端口")
		}
		r := &route{name: rc.Name, target: rc.Target, stats: &routeCounters{}}
		c.routes = append(c.routes, r)
		c.keys[idx].routes[sid] = r
		routeNames[rc.Name] = true
	}
	if cfg.Fallback != "" {
		if !validTarget(cfg.Fallback) {
			return nil, errors.New("fallback 必须是有效的主机和非零端口")
		}
		c.fallback = &route{name: "fallback", target: cfg.Fallback, stats: &routeCounters{}}
		c.routes = append(c.routes, c.fallback)
	}
	// 指纹只保留公钥及非私钥配置，实际私钥内容不序列化。
	b, _ := json.Marshal(struct {
		Config Config
		Public []string
	}{cfg, publicIDs})
	c.fingerprint = sha256.Sum256(b)
	return c, nil
}

func loadPrivateKey(k KeyConfig) (*ecdh.PrivateKey, error) {
	if (k.PrivateKeyEnv == "") == (k.PrivateKeyFile == "") {
		return nil, errors.New("每组密钥必须且只能引用一个环境变量或私钥文件")
	}
	var text string
	if k.PrivateKeyEnv != "" {
		text = os.Getenv(k.PrivateKeyEnv)
	} else {
		f, err := openPrivateKeyFile(k.PrivateKeyFile)
		if err != nil {
			return nil, errors.New("无法打开引用的私钥文件")
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0) {
			return nil, errors.New("私钥文件必须是普通文件，且只允许所有者访问")
		}
		b, err := io.ReadAll(io.LimitReader(f, 257))
		if err != nil || len(b) > 256 {
			return nil, errors.New("私钥文件读取失败或内容过长")
		}
		text = string(b)
	}
	text = strings.TrimSpace(text)
	if len(text) > 128 {
		return nil, errors.New("引用的 Reality 私钥格式无效")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(text, "="))
	if err != nil || len(b) != 32 {
		return nil, errors.New("引用的 Reality 私钥必须是 32 字节的 URL-safe Base64")
	}
	private, err := ecdh.X25519().NewPrivateKey(b)
	clear(b)
	if err != nil {
		return nil, errors.New("引用的 Reality 私钥无效")
	}
	return private, nil
}

func parseShortID(s string) ([8]byte, error) {
	var id [8]byte
	if len(s) == 0 || len(s) > 16 || len(s)%2 != 0 {
		return id, errors.New("short_id 必须为 2 至 16 位、长度为偶数的十六进制文本")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, errors.New("short_id 格式无效")
	}
	copy(id[:], b)
	return id, nil
}

func validName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validServerName(s string) bool {
	if len(s) == 0 || len(s) > 253 || strings.HasSuffix(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validTarget(target string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return false
	}
	p, err := strconv.ParseUint(port, 10, 16)
	return err == nil && p > 0 && (net.ParseIP(host) != nil || validServerName(strings.ToLower(host)))
}
