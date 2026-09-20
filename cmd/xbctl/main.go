// 旧升级器固定下载此附件；新安装的管理入口为 yz-agent。
package main

import (
	"fmt"
	"os"

	"github.com/P0me1oo/YZ-Agent/internal/agentcli"
	"github.com/P0me1oo/YZ-Agent/internal/buildinfo"
)

var (
	version   = "v1.15.1"
	buildTime = "unknown"
	commit    = "unknown"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "version" || args[0] == "-v" || args[0] == "--version") {
		fmt.Println(buildinfo.Report("xbctl", version, buildTime, commit))
		return
	}
	if err := agentcli.Run(args, version, buildTime, commit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
