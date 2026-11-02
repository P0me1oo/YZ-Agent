package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEveryIntervalRejectsNegativeAndOverflowValues(t *testing.T) {
	for _, section := range []string{"node", "ws"} {
		var fields reflect.Type
		if section == "node" {
			fields = reflect.TypeOf(NodeConfig{})
		} else {
			fields = reflect.TypeOf(WSConfig{})
		}
		for index := 0; index < fields.NumField(); index++ {
			field := fields.Field(index)
			t.Run(section+"."+field.Name, func(t *testing.T) {
				for _, invalid := range []int64{-1, 1 << 62} {
					node, ws := NodeConfig{}, WSConfig{}
					if section == "node" {
						reflect.ValueOf(&node).Elem().Field(index).SetInt(invalid)
					} else {
						reflect.ValueOf(&ws).Elem().Field(index).SetInt(invalid)
					}
					if err := ValidateIntervals(node, ws); err == nil {
						t.Fatalf("%s 允许 %d", field.Name, invalid)
					}
				}
			})
		}
	}
	if err := ValidateIntervals(NodeConfig{}, WSConfig{}); err != nil {
		t.Fatal("零值未保留默认语义")
	}
	if err := ValidateIntervals(NodeConfig{TrackInterval: 86400}, WSConfig{HandshakeTimeout: 300, BackoffInitial: 3600, BackoffMax: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIntervals(NodeConfig{}, WSConfig{BackoffInitial: 61}); err == nil {
		t.Fatal("未校验默认最大退避与初始值关系")
	}
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("panel:\n  url: https://panel.example.test\n  token: test\n  node_id: 1\nnode:\n  track_interval: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("无效采样周期通过配置加载")
	}
}

func TestExpandedNodesHaveSeparateCertificateStorage(t *testing.T) {
	c := &Config{InstanceID: "multi", Kernel: KernelConfig{ConfigDir: t.TempDir()}, Nodes: []NodeEntry{{NodeID: 1}, {NodeID: 2, Kernel: &KernelOverride{LogLevel: "debug"}}}}
	c.setDefaultsFrom(c.Kernel.ConfigDir)
	nodes := c.ExpandNodes()
	if nodes[0].Cert.CertDir == nodes[1].Cert.CertDir || nodes[0].Kernel.ConfigDir == nodes[1].Kernel.ConfigDir {
		t.Fatal("多节点共享默认存储")
	}
	for _, node := range nodes {
		if node.Cert.CertDir != filepath.Join(node.Kernel.ConfigDir, "certs") {
			t.Fatal("证书默认目录未跟随节点")
		}
	}
}

func TestStandaloneLayoutUsesInstanceIdentity(t *testing.T) {
	a := &Config{InstanceID: "local-a", Kernel: KernelConfig{ConfigDir: filepath.Join(t.TempDir(), "a")}, Standalone: &StandaloneConfig{Enabled: true}}
	b := &Config{InstanceID: "local-b", Kernel: KernelConfig{ConfigDir: filepath.Join(t.TempDir(), "b")}, Standalone: &StandaloneConfig{Enabled: true}}
	if err := ValidateStartupLayout([]*Config{a, b}); err != nil {
		t.Fatal(err)
	}
	autoA, err := a.AutoInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	autoB, err := b.AutoInstanceID()
	if err != nil || autoA == autoB {
		t.Fatal("目录独立的本地实例生成相同身份")
	}
	b.InstanceID = a.InstanceID
	if err := ValidateStartupLayout([]*Config{a, b}); err == nil {
		t.Fatal("重复实例身份未拒绝")
	}
	b.InstanceID = "local-b"
	b.Kernel.ConfigDir = a.Kernel.ConfigDir
	if err := ValidateStartupLayout([]*Config{a, b}); err == nil {
		t.Fatal("独立运行绕过目录冲突")
	}
	b.Kernel.ConfigDir = "other"
	a.Standalone, b.Standalone = nil, nil
	a.Panel, b.Panel = PanelConfig{URL: "https://panel.test", NodeID: 1}, PanelConfig{URL: "https://panel.test", NodeID: 1}
	if err := ValidateStartupLayout([]*Config{a, b}); err == nil {
		t.Fatal("真实面板重复绑定未拒绝")
	}
}
