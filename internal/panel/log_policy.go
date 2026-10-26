package panel

import "github.com/P0me1oo/YZ-Agent/internal/nlog"

type LogPolicy struct {
	Version  int  `json:"version"`
	Disabled bool `json:"disabled"`
}

func (c *Client) applyLogPolicy(policy *LogPolicy) {
	if policy != nil && policy.Version == 1 {
		nlog.ApplyLogPolicy(nlog.LogPolicyKey(c.baseURL, c.machineID, c.nodeID), policy.Disabled)
	}
}
