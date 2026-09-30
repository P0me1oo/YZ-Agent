//go:build !linux

package systemwg

import "github.com/P0me1oo/YZ-Agent/internal/model"

// 非 Linux 平台继续使用核心原有实现；服务器自动迁移仅适用于 Linux。
type Runtime struct{}

func (*Runtime) Close()                                                       {}
func (*Runtime) Start() error                                                 { return nil }
func Prepare(_ *model.NodeSpec, _ string, _ map[string]any) (*Runtime, error) { return &Runtime{}, nil }
