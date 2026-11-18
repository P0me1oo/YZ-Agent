package cert

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/cert/dnsproviders"
	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func RestrictPanelConfig(remote *config.CertConfig, local config.CertConfig) error {
	switch resolveModeFor(*remote) {
	case "file":
		if local.CertFile != "" && local.KeyFile != "" && remote.CertFile == local.CertFile && remote.KeyFile == local.KeyFile {
			return nil
		}
		for _, path := range []string{remote.CertFile, remote.KeyFile} {
			rel, err := filepath.Rel(remote.CertDir, path)
			if remote.CertDir == "" || err != nil || !filepath.IsLocal(rel) || rel == "." {
				return errors.New("面板证书文件必须位于本机证书目录或由本机配置明确指定")
			}
		}
		remote.RestrictFilesToCertDir = true
	case "dns":
		provider, ok := dnsproviders.Get(remote.DNSProvider)
		if !ok {
			return nil // 由证书管理器给出支持的提供商列表。
		}
		localProvider, same := dnsproviders.Get(local.DNSProvider)
		if same && localProvider == provider && len(remote.DNSEnv) == 0 {
			// 本地明确选择的提供商可继续使用本机凭据和区域设置。
			remote.DNSEnv = local.DNSEnv
			return nil
		}
		for name := range remote.DNSEnv {
			switch strings.ToUpper(name) {
			case "NAMECHEAP_API_ENDPOINT", "OVH_ENDPOINT", "AWS_PROFILE", "GCE_SERVICE_ACCOUNT_FILE", "GOOGLE_APPLICATION_CREDENTIALS":
				return errors.New("自定义 DNS 接口和凭据文件须由本机配置授权")
			}
		}
		var required []string
		switch provider.Names[0] {
		case "route53":
			required = []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}
		case "azure":
			required = []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET"}
		case "googleclouddns":
			required = []string{"GCE_SERVICE_ACCOUNT_JSON"}
		}
		for _, name := range required {
			if strings.TrimSpace(remote.DNSEnv[name]) == "" {
				return errors.New("面板 DNS 配置须提供显式凭据，本机默认凭据需要本机授权")
			}
		}
	}
	return nil
}
