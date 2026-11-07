package cert

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"
)

// Status 只包含公开证书元数据，不上报私钥、DNS 凭据或原始错误。
type Status struct {
	Domain       string `json:"domain"`
	Issuer       string `json:"issuer"`
	Fingerprint  string `json:"fingerprint"`
	NotAfter     int64  `json:"not_after"`
	NotBefore    int64  `json:"not_before"`
	Present      bool   `json:"present"`
	Applied      bool   `json:"applied"`
	Mode         string `json:"mode"`
	RenewalError string `json:"renewal_error,omitempty"`
	Updated      int64  `json:"updated_at"`
}

func (m *Manager) Status() Status {
	out := Status{}
	if value := m.status.Load(); value != nil {
		out = *value
	}
	material := m.mat.Load()
	if material == nil {
		return out
	}
	block, _ := pem.Decode(material.certPEM)
	if block == nil {
		return out
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return out
	}
	sum := sha256.Sum256(leaf.Raw)
	out.Present = true
	out.Domain = leaf.Subject.CommonName
	if len(leaf.DNSNames) > 0 {
		out.Domain = leaf.DNSNames[0]
	}
	out.Issuer = leaf.Issuer.CommonName
	out.Fingerprint = hex.EncodeToString(sum[:])
	out.NotAfter = leaf.NotAfter.Unix()
	out.NotBefore = leaf.NotBefore.Unix()
	return out
}
func (m *Manager) renewalStatus(mode, message string) {
	m.status.Store(&Status{Mode: mode, RenewalError: message, Updated: time.Now().Unix()})
}
func (m *Manager) Renew(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.magic == nil || !m.acmeStarted {
		return fmt.Errorf("当前证书不支持 ACME 续期")
	}
	if err := m.magic.RenewCertSync(ctx, m.cfg.Domain, true); err != nil {
		m.renewalStatus(resolveModeFor(m.cfg), "续期失败，请检查验证设置与节点日志")
		return err
	}
	// 强制续期后重新读取，事件没有回调也不会漏掉新证书。
	cert, key, err := readPEMFromStorage(ctx, m.magic.Storage, m.magic.Issuers[0].IssuerKey(), m.cfg.Domain)
	if err != nil {
		return err
	}
	if !m.publishRenewal(m.cfg, m.generation.Load(), cert, key) {
		return fmt.Errorf("证书配置已变化")
	}
	m.renewalStatus(resolveModeFor(m.cfg), "")
	return nil
}
