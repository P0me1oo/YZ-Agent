package installer

import _ "embed"

// Script 与命令行安装器共用迁移和失败恢复流程。
//
//go:embed install.sh
var Script string
