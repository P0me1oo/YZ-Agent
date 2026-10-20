package firewall

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func (b *systemBackend) ufwLegacyComment(rule Rule) string {
	sum := sha256.Sum256([]byte(rule.key()))
	return fmt.Sprintf("%s:%x", b.nftOwner(), sum[:6])
}

func (b *systemBackend) ufwComment(rule Rule) string {
	legacy := b.ufwLegacyComment(rule)
	ids := b.ufwNodeIDs(rule)
	if len(ids) == 0 {
		return legacy
	}
	label := "node="
	if len(ids) > 1 {
		label = "nodes="
	}
	namespace, suffix, _ := strings.Cut(legacy, ":")
	return namespace + ":" + label + strings.Join(ids, ",") + ":" + suffix
}

// 节点归属来自服务层的实例/节点标识，不从注释反推运行配置。
func (b *systemBackend) ufwNodeIDs(rule Rule) []string {
	ids := map[int]bool{}
	for owner, plan := range b.plans {
		separator := strings.LastIndex(owner, "/node/")
		if separator < 0 {
			continue
		}
		id, err := strconv.Atoi(owner[separator+len("/node/"):])
		if err != nil || id <= 0 {
			continue
		}
		for _, claim := range append(slices.Clone(plan.Listeners), plan.Redirects...) {
			claim.Target = 0
			if claim.Family == rule.Family && claim.Address == rule.Address && claim.Protocol == rule.Protocol &&
				claim.Source == rule.Source && claim.Ports.Overlaps(rule.Ports) {
				ids[id] = true
			}
		}
	}
	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	slices.Sort(ordered)
	result := make([]string, 0, len(ordered))
	for _, id := range ordered {
		result = append(result, strconv.Itoa(id))
	}
	return result
}

// 节点列表只是显示信息；旧注释和节点增删前的注释仍以实例及规则摘要确认归属。
func (b *systemBackend) ownsUFWComment(rule Rule, comment string) bool {
	legacy := b.ufwLegacyComment(rule)
	if comment == legacy {
		return true
	}
	namespace, rest, found := strings.Cut(comment, ":")
	label, suffix, labelled := strings.Cut(rest, ":")
	if !found || !labelled || namespace+":"+suffix != legacy {
		return false
	}
	raw, single := strings.CutPrefix(label, "node=")
	if !single {
		var multiple bool
		raw, multiple = strings.CutPrefix(label, "nodes=")
		if !multiple {
			return false
		}
	}
	ids := strings.Split(raw, ",")
	if (single && len(ids) != 1) || (!single && len(ids) < 2) {
		return false
	}
	previous := 0
	for _, value := range ids {
		id, err := strconv.Atoi(value)
		if err != nil || id <= previous || strconv.Itoa(id) != value {
			return false
		}
		previous = id
	}
	return true
}
