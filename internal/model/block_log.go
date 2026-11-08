package model

import (
	"strconv"
	"strings"
)

// 每条面板禁止访问规则使用独立的黑洞出站，沿用核心实际选路结果。
// 不重做域名、地址或协议匹配，内置和自定义拦截不冒充面板规则。
const panelBlockPrefix = "yz-panel-block-"

func PanelBlockTag(id int) string { return panelBlockPrefix + strconv.Itoa(id) }

func PanelBlockID(tag string) int {
	if !strings.HasPrefix(tag, panelBlockPrefix) {
		return 0
	}
	id, err := strconv.Atoi(strings.TrimPrefix(tag, panelBlockPrefix))
	if err != nil || id <= 0 || tag != PanelBlockTag(id) {
		return 0
	}
	return id
}
