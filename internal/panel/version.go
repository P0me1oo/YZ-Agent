package panel

// VersionGate 由所属状态循环串行使用，已经退出的基线不能被迟到消息重新启用。
type VersionGate struct {
	current StateVersion
	retired map[string]bool
}

func (g *VersionGate) Current() StateVersion { return g.current }

func (g *VersionGate) Accept(version StateVersion) bool {
	if version.Epoch == "" {
		return g.current.Epoch == ""
	}
	if version.Sequence == 0 || g.retired[version.Epoch] {
		return false
	}
	if version.Epoch == g.current.Epoch {
		if version.Sequence < g.current.Sequence {
			return false
		}
	} else if g.current.Epoch != "" {
		if g.retired == nil {
			g.retired = make(map[string]bool)
		}
		g.retired[g.current.Epoch] = true
	}
	g.current = version
	return true
}
