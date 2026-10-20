package firewall

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// sourcePlan 仅持久化监听范围和来源集合，用于异常退出后的恢复与精确清理。
type sourcePlan struct {
	Owner     string   `json:"owner"`
	Listeners []Rule   `json:"listeners"`
	Blocked   []string `json:"blocked,omitempty"`
}

func (b *systemBackend) sourceID() string {
	sum := sha256.Sum256([]byte(b.scope))
	return fmt.Sprintf("%x", sum[:6])
}

func (b *systemBackend) sourceOwner() string { return "yz-agent:source:" + b.sourceID() }
func (b *systemBackend) sourceTable() string { return "yz_source_" + b.sourceID() }
func (b *systemBackend) sourceChain() string { return "YZ_SOURCE_" + b.sourceID() }

func (b *systemBackend) validateSourceState() error {
	if b.state.SourceDriver != "" && b.state.SourceDriver != "nftables" && b.state.SourceDriver != "iptables" {
		return fmt.Errorf("无效的来源过滤后端记录")
	}
	if b.state.SourceDriver == "" && (len(b.state.SourcePlans) > 0 || len(b.state.SourceSets) > 0) {
		return fmt.Errorf("来源过滤记录缺少后端")
	}
	if b.state.SourceDriver != "iptables" && len(b.state.SourceSets) > 0 {
		return fmt.Errorf("来源集合记录与后端不匹配")
	}
	owners := map[string]bool{}
	for _, plan := range b.state.SourcePlans {
		if plan.Owner == "" || owners[plan.Owner] || len(plan.Listeners) == 0 {
			return fmt.Errorf("无效的来源过滤归属记录")
		}
		owners[plan.Owner] = true
		for _, rule := range plan.Listeners {
			if !rule.valid() || rule.Source != "" || rule.Target != 0 {
				return fmt.Errorf("无效的来源过滤监听记录")
			}
		}
		for _, raw := range plan.Blocked {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
				return fmt.Errorf("无效的来源过滤网段记录")
			}
		}
	}
	for _, name := range b.state.SourceSets {
		if !b.ownsSourceSetName(name) {
			return fmt.Errorf("无效的来源 IP 集合归属记录")
		}
	}
	return nil
}

// ApplySources 在端口放行及核心监听前执行。只安装丢弃规则，允许来源仍须经过系统原有规则。
func (b *systemBackend) ApplySources(ctx context.Context, plans map[string]Plan) error {
	desired := make(map[string]sourcePlan)
	if b.cfg.IsEnabled() {
		for owner, plan := range plans {
			if plan.Restricted {
				desired[owner] = sourcePlan{Owner: owner, Listeners: slices.Clone(plan.Listeners), Blocked: slices.Clone(plan.Blocked)}
			}
		}
		// 节点发现尚未完成时保留未认领的旧限制，避免先启动的节点撤销其他节点的保护。
		for _, old := range b.state.SourcePlans {
			_, known := plans[old.Owner]
			if !known && b.recovering {
				desired[old.Owner] = old
			}
		}
	}
	var ordered []sourcePlan
	for _, plan := range desired {
		ordered = append(ordered, plan)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Owner < ordered[j].Owner })
	driver := b.state.SourceDriver
	if len(ordered) > 0 && driver == "" {
		var err error
		driver, err = b.selectSourceDriver(ctx)
		if err != nil {
			return err
		}
		b.state.SourceDriver = driver
		// 先记录意图，创建规则后即使异常退出也有足够信息清理。
		if err := b.save(); err != nil {
			return err
		}
	}
	if driver == "" {
		return nil
	}
	var err error
	switch driver {
	case "nftables":
		err = b.syncSourceNFT(ctx, ordered)
	case "iptables":
		err = b.syncSourceIPTables(ctx, ordered)
	default:
		err = fmt.Errorf("未知的来源过滤后端")
	}
	if err != nil {
		return fmt.Errorf("系统防火墙来源限制失败（%s）: %w", driver, err)
	}
	if len(ordered) > 0 && reflect.DeepEqual(b.state.SourcePlans, ordered) {
		return nil
	}
	b.state.SourcePlans = ordered
	if len(ordered) == 0 {
		b.state.SourceDriver = ""
	}
	return b.save()
}

func (b *systemBackend) selectSourceDriver(ctx context.Context) (string, error) {
	if !b.cfg.IsEnabled() {
		return "", fmt.Errorf("来源限制需要开启系统防火墙管理")
	}
	if configured := b.cfg.SourceBackend; configured != "" && configured != "auto" {
		if !sourceToolsAvailable(b.commands.Available, configured) {
			return "", errSourceToolsMissing
		}
		return configured, nil
	}
	if err := b.checkSourceEnvironment(ctx); err != nil {
		return "", err
	}
	if b.commands.Available("nft") {
		return "nftables", nil
	}
	if b.sourceIPTablesAvailable() {
		return "iptables", nil
	}
	return "", errSourceToolsMissing
}

func (b *systemBackend) checkSourceEnvironment(ctx context.Context) error {
	ufwActive, firewalldActive := false, false
	if b.commands.Available("ufw") {
		status, err := b.commands.Run(ctx, "ufw", "", "status")
		if err != nil {
			return fmt.Errorf("检查 UFW 来源过滤权限: %w", err)
		}
		if !strings.Contains(status, "Status: active") && !strings.Contains(status, "Status: inactive") {
			return fmt.Errorf("无法识别 UFW 状态")
		}
		ufwActive = strings.Contains(status, "Status: active")
	}
	if b.commands.Available("firewall-cmd") {
		status, err := b.commands.Run(ctx, "firewall-cmd", "", "--state")
		if strings.TrimSpace(status) == "running" && err == nil {
			firewalldActive = true
		} else if !firewalldInactive(status) {
			return fmt.Errorf("无法确认 firewalld 来源过滤权限或运行状态")
		}
	}
	if ufwActive && firewalldActive {
		return fmt.Errorf("UFW 和 firewalld 同时运行，请明确指定来源过滤后端并消除规则冲突")
	}
	return nil
}

func (b *systemBackend) sourceIPTablesAvailable() bool {
	return sourceToolsAvailable(b.commands.Available, "iptables")
}

func sourcePrefixes(plan sourcePlan, family int) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range plan.Blocked {
		prefix := netip.MustParsePrefix(raw)
		if prefix.Addr().Is4() == (family == 4) {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func sourcePlanID(plan sourcePlan) string {
	sum := sha256.Sum256([]byte(plan.Owner))
	return fmt.Sprintf("%x", sum[:8])
}
