package panel

import (
	"context"
	"errors"
	"math"
)

const scopedTrafficVersion = "scoped-v2"

func (c *Client) UsesScopedTraffic() bool { return c.scopedAccounting.Load() }

// ReportScopedTraffic sends only frozen accounting fields. Machine status
// and connection samples have their own sequence and are not replayed.
func (c *Client) ReportScopedTraffic(
	ctx context.Context, id string, direct, entry map[int][2]int64,
	routes map[int]map[int][2]int64, transport map[int][2]int64, events []LimitEvent,
) error {
	if !c.UsesScopedTraffic() {
		return errors.New("面板尚未确认分项计量协议")
	}
	payload := map[string]any{"report_id": id, "traffic_accounting": scopedTrafficVersion}
	if len(direct) > 0 {
		payload["direct_traffic"] = direct
	}
	if len(entry) > 0 {
		payload["entry_traffic"] = entry
	}
	if len(routes) > 0 {
		payload["relay_user_traffic"] = routes
	}
	if len(transport) > 0 {
		loads := make(map[int][2]int64, len(transport))
		for key, amount := range transport {
			if key == 0 {
				// The entry's own egress has no separate landing machine.
				continue
			}
			if key > 0 || key < -math.MaxInt32 {
				return errors.New("流量批次缺少面板签发的实际落地绑定，已保留待报数据")
			}
			loads[-key] = amount
		}
		if len(loads) > 0 {
			payload["relay_loads"] = loads
		}
	}
	if len(events) > 0 {
		payload["limit_events"] = events
	}
	return c.reportRealtime(ctx, payload)
}
