package model

import (
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestRelayPermissionsDecodeAndCopyPreserveEmptyAndLegacy(t *testing.T) {
	for _, input := range []string{`{"id":1}`, `{"id":1,"relay_routes":[]}`, `{"id":1,"relay_routes":[12,11]}`} {
		var wire panel.User
		if err := json.Unmarshal([]byte(input), &wire); err != nil {
			t.Fatal(err)
		}
		users := UserSpecsFromPanel([]panel.User{wire})
		copied := UserSpecsToPanel(users)
		if (wire.RelayRoutes == nil) != (copied[0].RelayRoutes == nil) || len(wire.RelayRoutes) != len(copied[0].RelayRoutes) {
			t.Fatal("复制改变权限语义")
		}
		if len(wire.RelayRoutes) > 0 {
			wire.RelayRoutes[0] = 99
			copied[0].RelayRoutes[0] = 98
			if users[0].RelayRoutes[0] != 12 {
				t.Fatal("权限数组没有独立复制")
			}
		}
	}
	legacy := UserSpec{}
	denied := UserSpec{RelayRoutes: []int{}}
	if !legacy.AllowsRelayRoute(11) || denied.AllowsRelayRoute(11) || legacy.RelayRoutesKey() == denied.RelayRoutesKey() {
		t.Fatal("旧版兼容与拒绝全部没有区分")
	}
	if (UserSpec{RelayRoutes: []int{12, 11, 12}}).RelayRoutesKey() != (UserSpec{RelayRoutes: []int{11, 12}}).RelayRoutesKey() {
		t.Fatal("权限顺序或重复项改变摘要")
	}
}
