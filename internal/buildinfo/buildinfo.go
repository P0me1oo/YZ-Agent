// Package buildinfo 提供 Node 构建和内嵌内核的可审计版本信息。
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

const (
	// Xray 上游和 YZ fork 的发布标识必须指向固定引用，不能使用移动分支。
	XrayUpstreamTag    = "v26.9.9"
	XrayUpstreamCommit = "52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120"
	XrayForkVersion    = "v26.8.0"
	XrayForkCommit     = "9fcf874e21147978c5117e4832c78b4adadd4320"

	// sing-box 的 require 版本和 replace 后实际使用的版本需要同时记录。
	SingBoxRequestedVersion = "v1.14.0"
	SingBoxResolvedVersion  = "v1.14.0-yz.2"
	SingBoxUpstreamCommit   = "0b8995879f29a9b98ee027bc17b75e101445b238"
	SingBoxForkCommit       = "09615a105e219076330d9d2a25ea1e2e733d5427"

	// SS2022 关闭补丁与 Node 同一提交发布，上游来源保持独立可查。
	ShadowsocksUpstreamVersion = "v0.2.8"
	ShadowsocksUpstreamCommit  = "e0612494bafdd1429e9632bc52fd278585d28690"

	// AnyTLS 关闭状态补丁随 Node 源码固定，上游版本单独标识。
	AnyTLSUpstreamVersion = "v0.0.11"
	AnyTLSUpstreamCommit  = "130d2e61b8895727bfed4942c535e91b246a9603"
)

// ModuleVersion 返回构建产物中嵌入的模块版本。
// 对于 replace 模块，同时返回替换路径和版本，便于在版本输出中确认实际 fork 提交。
func ModuleVersion(path string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unavailable"
	}
	for _, dep := range info.Deps {
		if dep.Path != path {
			continue
		}
		if dep.Replace != nil {
			if dep.Replace.Version == "" {
				return dep.Replace.Path
			}
			return fmt.Sprintf("%s@%s", dep.Replace.Path, dep.Replace.Version)
		}
		return fmt.Sprintf("%s@%s", dep.Path, dep.Version)
	}
	return "not-linked"
}

// Report 生成两个命令行程序共用的版本信息输出。
func Report(binary, version, buildTime, commit string) string {
	return fmt.Sprintf(
		"%s %s (built %s, commit %s)\n"+
			"xray-core: fork %s (upstream %s @ %s; fork commit %s; module %s)\n"+
			"sing-box: requested %s, resolved %s (upstream commit %s; fork commit %s; module %s)\n"+
			"sing-shadowsocks: upstream %s @ %s; Node compatibility source %s\n"+
			"anytls: upstream %s @ %s; Node compatibility source %s",
		binary,
		version,
		buildTime,
		commit,
		XrayForkVersion,
		XrayUpstreamTag,
		XrayUpstreamCommit,
		XrayForkCommit,
		ModuleVersion("github.com/xtls/xray-core"),
		SingBoxRequestedVersion,
		SingBoxResolvedVersion,
		SingBoxUpstreamCommit,
		SingBoxForkCommit,
		ModuleVersion("github.com/sagernet/sing-box"),
		ShadowsocksUpstreamVersion,
		ShadowsocksUpstreamCommit,
		ModuleVersion("github.com/sagernet/sing-shadowsocks"),
		AnyTLSUpstreamVersion,
		AnyTLSUpstreamCommit,
		ModuleVersion("github.com/anytls/sing-anytls"),
	)
}
