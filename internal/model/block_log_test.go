package model

import "testing"

func TestPanelBlockTagRejectsCustomCollisions(t *testing.T) {
	for _, tag := range []string{"yz-panel-block-1", "YZ-PANEL-BLOCK-2"} {
		if err := validateOutboundTagCollisions([]OutboundConfig{{Tag: tag}}, nil); err == nil {
			t.Fatal("未拒绝自定义出站占用规则标签")
		}
		if err := validateOutboundTagCollisions(nil, []outboundTagSource{{Tag: tag, Source: "test"}}); err == nil {
			t.Fatal("未拒绝原生出站占用规则标签")
		}
	}
	for _, tag := range []string{"block", "yz-panel-block-0", "yz-panel-block--1", "yz-panel-block-01", "yz-panel-block-1suffix"} {
		if PanelBlockID(tag) != 0 {
			t.Fatalf("误识别规则标签 %s", tag)
		}
	}
	if PanelBlockID(PanelBlockTag(123)) != 123 {
		t.Fatal("规则编号丢失")
	}
}
