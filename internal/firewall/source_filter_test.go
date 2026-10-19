package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type sourceCapabilities struct {
	available      map[string]bool
	ufw, firewalld string
	err            error
}

type sourceNFTReadFixture struct{ table, owner string }

func (sourceNFTReadFixture) Available(name string) bool { return name == "nft" }
func (r sourceNFTReadFixture) Run(_ context.Context, _ string, _ string, args ...string) (string, error) {
	if args[len(args)-1] == "tables" {
		return fmt.Sprintf(`{"nftables":[{"metainfo":{}},{"table":{"family":"ip","name":"filter"}},{"table":{"family":"inet","name":%q}}]}`, r.table), nil
	}
	return fmt.Sprintf(`{"nftables":[{"metainfo":{}},{"table":{"family":"inet","name":%q}},{"chain":{"family":"inet","table":%q,"name":"ownership"}},{"chain":{"family":"inet","table":%q,"name":"input","type":"filter","hook":"input","prio":-10,"policy":"accept"}},{"rule":{"family":"inet","table":%q,"chain":"ownership","comment":%q,"expr":[{"counter":{"packets":0,"bytes":0}}]}}]}`, r.table, r.table, r.table, r.table, r.owner), nil
}

func TestSourceNFTReadDoesNotRetainOtherFirewallTableObjects(t *testing.T) {
	b := &systemBackend{scope: "fixture"}
	b.commands = sourceNFTReadFixture{table: b.sourceTable(), owner: b.sourceOwner()}
	objects, exists, err := b.readSourceNFT(context.Background())
	if err != nil || !exists || !b.sourceNFTMatches(objects, sourceNFTSpec{Sets: map[string][]string{}, Rules: []map[string]any{}}) {
		t.Fatalf("其他防火墙表清单污染了本实例规则核验: %v", err)
	}
}

func (r sourceCapabilities) Available(name string) bool { return r.available[name] }
func (r sourceCapabilities) Run(_ context.Context, name, _ string, _ ...string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if name == "ufw" {
		return r.ufw, nil
	}
	if name == "firewall-cmd" {
		return r.firewalld, nil
	}
	return "", errors.New("测试未授权执行系统写入")
}

func TestSourceBackendSelectionAndPermissionFailure(t *testing.T) {
	iptables := map[string]bool{"ufw": true, "nft": true, "iptables": true, "ip6tables": true, "iptables-restore": true, "ip6tables-restore": true, "ipset": true}
	for _, test := range []struct {
		name   string
		runner sourceCapabilities
		want   string
	}{
		{"UFW", sourceCapabilities{available: iptables, ufw: "Status: active"}, "iptables"},
		{"UFW nft fallback", sourceCapabilities{available: map[string]bool{"ufw": true, "nft": true}, ufw: "Status: active"}, "nftables"},
		{"native nft", sourceCapabilities{available: map[string]bool{"nft": true}}, "nftables"},
		{"no tools", sourceCapabilities{}, ""},
		{"no permission", sourceCapabilities{available: iptables, err: errors.New("无权限")}, ""},
		{"conflicting managers", sourceCapabilities{available: map[string]bool{"ufw": true, "firewall-cmd": true, "nft": true}, ufw: "Status: active", firewalld: "running"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := &systemBackend{commands: test.runner}
			got, err := b.selectSourceDriver(context.Background())
			if test.want == "" {
				if err == nil {
					t.Fatal("不支持或无权限的环境被误判为可用")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("后端=%s，错误=%v", got, err)
			}
		})
	}
}

func TestSourcePlanRequiresPreparedSetAndRejectsUnsafeEnvironments(t *testing.T) {
	node := &model.NodeSpec{Protocol: "socks", ServerPort: 1080, SourcePolicy: &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"198.51.100.7"}}}
	if _, err := PlanForNode(node, "xray"); err == nil {
		t.Fatal("未准备集合也生成了防火墙规则")
	}
	node.SourcePolicyReady = true
	node.SourceBlockCIDRs = []string{"0.0.0.0/1", "::/0"}
	plan, err := PlanForNode(node, "xray")
	if err != nil || !plan.Restricted || len(plan.Listeners) != 4 || len(plan.Blocked) != 2 {
		t.Fatalf("双栈 SOCKS 计划不完整: %+v %v", plan, err)
	}
	for _, m := range []*Manager{{enabled: false}, {enabled: true, linux: false, plans: map[string]Plan{}}, {enabled: true, linux: true, plans: map[string]Plan{}}} {
		if err := m.Apply(context.Background(), "node", node, "xray"); err == nil {
			t.Fatal("不具备来源过滤能力却返回成功")
		}
	}
	conflict := map[string]Plan{"protected": plan, "public": {Listeners: plan.Listeners}}
	if _, _, err := combinePlans(conflict); err == nil {
		t.Fatal("受保护节点与公开节点的共用端口未拒绝")
	}
}

func TestSourceIPSetHandlesEntireAddressFamilyAndScopedRules(t *testing.T) {
	b := &systemBackend{scope: "test"}
	plan := sourcePlan{Owner: "node", Listeners: []Rule{{Family: 4, Address: "192.0.2.1", Protocol: "tcp", Ports: portset.Range{From: 1080, To: 1080}}, {Family: 6, Protocol: "tcp", Ports: portset.Range{From: 1080, To: 1080}}}, Blocked: []string{"0.0.0.0/0", "::/0"}}
	for _, family := range []int{4, 6} {
		prefixes := ipsetSourcePrefixes(plan, family)
		if len(prefixes) != 2 || strings.Contains(strings.Join(prefixes, ","), "/0") {
			t.Fatal("hash:net 的 /0 没有被等价拆分")
		}
		name := b.sourceSetName(prefixes, family)
		if !b.ownsSourceSetName(name) || !b.ownsSourceSetName(name+"_t") {
			t.Fatal("集合名称不能安全记录或恢复")
		}
		for _, line := range b.sourceIPTablesRules([]sourcePlan{plan}, family) {
			if strings.Contains(line, "DROP") && (!strings.Contains(line, "--dport 1080") || !strings.Contains(line, "--match-set "+name+" src")) {
				t.Fatal("来源规则扩大到了节点端口之外")
			}
		}
	}
}

func TestSourceNFTVerificationDetectsChangedRulesAndPrefixes(t *testing.T) {
	b := &systemBackend{scope: "test"}
	plans := []sourcePlan{{Owner: "node", Listeners: []Rule{{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 1080, To: 1080}}}, Blocked: []string{"198.51.100.0/24"}}}
	spec := b.sourceNFTSpec(plans)
	var script struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(b.sourceNFTScript(spec, false)), &script); err != nil {
		t.Fatal(err)
	}
	var objects []map[string]json.RawMessage
	for _, command := range script.NFTables {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(command["add"], &object); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	if !b.sourceNFTMatches(objects, spec) {
		t.Fatal("完整规则未通过核验")
	}
	for index, object := range objects {
		if object["rule"] == nil {
			continue
		}
		var rule map[string]any
		_ = json.Unmarshal(object["rule"], &rule)
		if rule["chain"] != "input" {
			continue
		}
		rule["expr"] = []any{map[string]any{"accept": nil}}
		data, _ := json.Marshal(rule)
		objects[index] = map[string]json.RawMessage{"rule": data}
		if b.sourceNFTMatches(objects, spec) {
			t.Fatal("来源拒绝被改成放行后仍通过核验")
		}
		break
	}
}

func TestSourceStateRejectsForeignResources(t *testing.T) {
	b := &systemBackend{scope: "test", state: savedState{SourceDriver: "iptables", SourceSets: []string{"administrator-set"}}}
	if b.validateSourceState() == nil {
		t.Fatal("接受了非本实例的集合归属")
	}
	if err := config.ValidateFirewall(config.FirewallConfig{SourceBackend: "nftables"}); err != nil {
		t.Fatal(err)
	}
}
