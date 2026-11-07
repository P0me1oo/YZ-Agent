package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"maps"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/P0me1oo/YZ-Agent/internal/cert/dnsproviders"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// Manager handles TLS certificate lifecycle.
//
// Supported modes (CertConfig.CertMode):
//   - "http"    — ACME HTTP-01 challenge via certmagic (needs port 80).
//   - "dns"     — ACME DNS-01 challenge via certmagic + libdns provider.
//   - "self"    — Self-signed certificate generated in memory.
//   - "file"    — User-provided certificate and key file paths (CertFile/KeyFile).
//   - "content" — Certificate and key PEM content pushed from the panel.
//   - "none"    — No TLS. The node will handle plain connections.
//
// All modes normalise to in-memory PEM. Kernels never see file paths —
// they always receive PEM bytes via TLSCert().
//
// Priority Logic (when CertMode is empty):
//
//  1. "http" (if auto_tls is true)
//  2. "content" (if both CertContent and KeyContent are provided)
//  3. "file" (if both CertFile and KeyFile paths are provided)
//  4. "none" (default)
type Manager struct {
	status      atomic.Pointer[Status]
	fileSources map[string]bool
	opMu        sync.Mutex
	materialMu  sync.Mutex
	generation  atomic.Uint64
	cache       *certmagic.Cache
	cfg         config.CertConfig

	// Atomic so the ACME renewal goroutine can swap without racing readers.
	mat atomic.Pointer[certMaterial]

	magic           *certmagic.Config
	renewed         atomic.Bool
	acmeStarted     bool
	acmeFingerprint string
	acmeCancel      context.CancelFunc
}

// certMaterial is an immutable snapshot of PEM-encoded cert + key.
type certMaterial struct {
	certPEM []byte
	keyPEM  []byte
}

// NewManager creates a certificate manager.
func NewManager(cfg config.CertConfig) *Manager {
	cfg.DNSEnv = maps.Clone(cfg.DNSEnv)
	return &Manager{cfg: cfg}
}

// storePEM atomically swaps the in-memory cert material and persists to disk
// so that restarts can reload without re-generating or re-requesting.
func (m *Manager) storePEM(cert, key []byte) {
	m.materialMu.Lock()
	defer m.materialMu.Unlock()
	m.publishPEM(m.cfg, cert, key)
}

// publishPEM 在发布锁内写入完整证书组，内存读者也只看到完整组。
func (m *Manager) publishPEM(cfg config.CertConfig, cert, key []byte) {
	m.renewalStatus(resolveModeFor(cfg), "")
	m.mat.Store(&certMaterial{certPEM: append([]byte(nil), cert...), keyPEM: append([]byte(nil), key...)})
	if cfg.CertDir == "" {
		return
	}
	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		nlog.Core().Warn("cert: create storage failed", "error", err)
		return
	}
	data, err := json.Marshal(persistedPair{Cert: cert, Key: key, Mode: resolveModeFor(cfg)})
	if err == nil {
		err = atomicWriteFile(filepath.Join(cfg.CertDir, "cert-pair.json"), data, 0o600)
	}
	if err != nil {
		nlog.Core().Warn("cert: persist pair failed", "error", err)
		return
	}
	// 旧双文件只作为迁移来源，成功发布后移除，避免旧版本材料被再次选中。
	for _, name := range []string{"cert.pem", "key.pem"} {
		path := filepath.Join(cfg.CertDir, name)
		if path == cfg.CertFile || path == cfg.KeyFile || m.fileSources[path] {
			continue
		}
		_ = os.Remove(path)
	}
}

type persistedPair struct {
	Cert []byte `json:"cert"`
	Key  []byte `json:"key"`
	Mode string `json:"mode,omitempty"`
}

// atomicWriteFile writes data to a temp file and renames, preventing partial reads.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return privacy.WriteFile(path, data, perm)
}

// loadPersistedPEM loads previously persisted cert material from cert_dir.
// Returns true if valid material was loaded.
func (m *Manager) loadPersistedPEM() bool {
	dir := m.cfg.CertDir
	if dir == "" {
		return false
	}
	data, err := privacy.ReadFile(filepath.Join(dir, "cert-pair.json"))
	var pair persistedPair
	if err == nil {
		if json.Unmarshal(data, &pair) != nil {
			return false
		}
		if pair.Mode != "" && pair.Mode != m.resolveMode() {
			return false
		}
	} else {
		// 已存在但无法读取的新格式不能退回旧文件。
		if !os.IsNotExist(err) {
			return false
		}
		pair.Cert, err = privacy.ReadFile(filepath.Join(dir, "cert.pem"))
		if err != nil {
			return false
		}
		pair.Key, err = privacy.ReadFile(filepath.Join(dir, "key.pem"))
		if err != nil {
			return false
		}
	}
	domain := m.cfg.Domain
	if domain == "" && m.resolveMode() == "self" {
		domain = "localhost"
	}
	if err := validateCertificate(pair.Cert, pair.Key, domain); err != nil {
		nlog.Core().Warn("cert: persisted certificate rejected", "error", err)
		return false
	}
	m.storePEM(pair.Cert, pair.Key)
	return true
}

// Reconfigure applies a new cert configuration at runtime (e.g. from panel push).
// Returns true when the PEM material changed (caller should restart kernel).
func (m *Manager) Reconfigure(ctx context.Context, newCfg config.CertConfig) (bool, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	oldTLS := m.TLSCert()
	if newCfg.CertDir == "" {
		newCfg.CertDir = m.cfg.CertDir
	}

	// If ACME is running and the new config materially differs (or switches
	// away from ACME), tear down the old certmagic instance first.
	if m.acmeStarted {
		newFp := acmeFingerprint(newCfg)
		newMode := resolveModeFor(newCfg)
		if newMode != "http" && newMode != "dns" || newFp != m.acmeFingerprint {
			m.tearDownACME()
		}
	}

	newCfg.DNSEnv = maps.Clone(newCfg.DNSEnv)
	m.cfg = newCfg

	if err := m.start(ctx); err != nil {
		return false, fmt.Errorf("cert reconfigure: %w", err)
	}

	newTLS := m.TLSCert()
	return !pemEqual(oldTLS, newTLS), nil
}

// tearDownACME cancels the running certmagic background goroutines and clears
// related state so a fresh ACME init can run.
func (m *Manager) tearDownACME() {
	m.generation.Add(1)
	if m.acmeCancel != nil {
		m.acmeCancel()
		m.acmeCancel = nil
	}
	if m.cache != nil {
		m.cache.Stop()
		m.cache = nil
	}
	m.magic = nil
	m.acmeStarted = false
	m.acmeFingerprint = ""
	m.materialMu.Lock()
	m.mat.Store(nil)
	m.renewed.Store(false)
	m.materialMu.Unlock()
}

// acmeFingerprint produces a stable string capturing every config field that
// requires re-running ACME when changed.
func acmeFingerprint(cfg config.CertConfig) string {
	keys := make([]string, 0, len(cfg.DNSEnv))
	for k := range cfg.DNSEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(cfg.CertMode)))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(cfg.Domain))
	fmt.Fprintf(&b, "|%s|%d|%t|", cfg.CertDir, cfg.HTTPPort, cfg.AutoTLS)
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(cfg.Email))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(cfg.DNSProvider))
	b.WriteByte('|')
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(cfg.DNSEnv[k])
		b.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// resolveModeFor mirrors (*Manager).resolveMode() for an arbitrary cfg, used
// by Reconfigure to decide tear-down without mutating manager state.
func resolveModeFor(cfg config.CertConfig) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.CertMode))
	if mode != "" {
		return mode
	}
	if cfg.AutoTLS {
		return "http"
	}
	if cfg.CertContent != "" && cfg.KeyContent != "" {
		return "content"
	}
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		return "file"
	}
	return "none"
}

// resolveMode returns the effective cert mode, handling backward compat for auto_tls.
func (m *Manager) resolveMode() string {
	mode := strings.ToLower(strings.TrimSpace(m.cfg.CertMode))
	if mode != "" {
		return mode
	}
	// Backward compat: auto_tls: true → "http"
	if m.cfg.AutoTLS {
		return "http"
	}
	// Inline PEM content provided → "content"
	if m.cfg.CertContent != "" && m.cfg.KeyContent != "" {
		return "content"
	}
	// If cert/key file paths are provided → "file"
	if m.cfg.CertFile != "" && m.cfg.KeyFile != "" {
		return "file"
	}
	return "none"
}

func (m *Manager) HasCert() bool {
	mat := m.mat.Load()
	return mat != nil && len(mat.certPEM) > 0 && len(mat.keyPEM) > 0
}
func (m *Manager) CertRenewed() bool { return m.renewed.Swap(false) }

// TLSCert returns the current PEM material as a kernel.TLSCert.
func (m *Manager) TLSCert() kernel.TLSCert {
	mat := m.mat.Load()
	if mat == nil {
		return kernel.TLSCert{}
	}
	return kernel.TLSCert{CertPEM: mat.certPEM, KeyPEM: mat.keyPEM}
}

// pemEqual reports whether two TLSCert values carry identical PEM content.
func pemEqual(a, b kernel.TLSCert) bool {
	return string(a.CertPEM) == string(b.CertPEM) && string(a.KeyPEM) == string(b.KeyPEM)
}

// Start initializes the cert manager based on the resolved mode.
func (m *Manager) Start(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.start(ctx)
}

func (m *Manager) start(ctx context.Context) error {
	mode := m.resolveMode()

	switch mode {
	case "none", "":
		m.materialMu.Lock()
		defer m.materialMu.Unlock()
		m.mat.Store(nil)
		m.renewed.Store(false)
		if m.cfg.CertDir != "" {
			preserveFiles := false
			if data, err := privacy.ReadFile(filepath.Join(m.cfg.CertDir, "cert-pair.json")); err == nil {
				var pair persistedPair
				if json.Unmarshal(data, &pair) == nil && pair.Mode == "file" {
					preserveFiles = true
				}
			}
			for _, name := range []string{"cert-pair.json", "cert.pem", "key.pem"} {
				path := filepath.Join(m.cfg.CertDir, name)
				if name != "cert-pair.json" && (preserveFiles || path == m.cfg.CertFile || path == m.cfg.KeyFile || m.fileSources[path]) {
					continue
				}
				if err := os.Remove(filepath.Join(m.cfg.CertDir, name)); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("clear active certificate: %w", err)
				}
			}
		}
		return nil
	case "file":
		return m.startFile()
	case "self":
		return m.startSelfSigned()
	case "content":
		return m.startContent()
	case "http":
		return m.startACME(ctx, nil)
	case "dns":
		solver, err := m.buildDNSSolver()
		if err != nil {
			return fmt.Errorf("build dns solver: %w", err)
		}
		return m.startACME(ctx, solver)
	default:
		return fmt.Errorf("unknown cert_mode: %q (supported: http, dns, self, file, content, none)", mode)
	}
}

// Stop 停止缓存维护任务；代次屏障阻止旧续期发布到新配置。
func (m *Manager) Stop() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.tearDownACME()
}

// ─── Mode: file ────────────────────────────────────────────────────────────

func (m *Manager) startFile() error {
	certPEM, err := privacy.ReadFile(m.cfg.CertFile)
	if err != nil {
		return fmt.Errorf("cert file: %w", err)
	}
	keyPEM, err := privacy.ReadFile(m.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("key file: %w", err)
	}
	if err := validateCertificate(certPEM, keyPEM, m.cfg.Domain); err != nil {
		return fmt.Errorf("invalid certificate pair: %w", err)
	}
	if m.fileSources == nil {
		m.fileSources = make(map[string]bool)
	}
	m.fileSources[m.cfg.CertFile], m.fileSources[m.cfg.KeyFile] = true, true
	m.storePEM(certPEM, keyPEM)
	nlog.Core().Debug("TLS certificate loaded from files", "cert", m.cfg.CertFile, "key", m.cfg.KeyFile)
	return nil
}

// ─── Mode: self ────────────────────────────────────────────────────────────

func (m *Manager) startSelfSigned() error {
	// Reuse persisted self-signed cert if available and valid.
	if m.loadPersistedPEM() {
		return nil
	}

	domain := m.cfg.Domain
	if domain == "" {
		domain = "localhost"
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// Add SANs
	if ip := net.ParseIP(domain); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{domain}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	m.storePEM(certPEM, keyPEM)
	nlog.Core().Info("self-signed certificate generated (in-memory)", "domain", domain, "valid_years", 10)
	return nil
}

// ─── Mode: content ────────────────────────────────────────────────────────

// startContent stores panel-supplied PEM strings in memory for inline use.
func (m *Manager) startContent() error {
	if m.cfg.CertContent == "" || m.cfg.KeyContent == "" {
		// No fresh content from panel — try previously persisted material.
		if m.loadPersistedPEM() {
			return nil
		}
		return fmt.Errorf("cert_mode 'content' requires both cert_content and key_content")
	}
	certPEM := []byte(m.cfg.CertContent)
	keyPEM := []byte(m.cfg.KeyContent)
	if err := validateCertificate(certPEM, keyPEM, m.cfg.Domain); err != nil {
		return fmt.Errorf("invalid certificate content: %w", err)
	}
	m.storePEM(certPEM, keyPEM)
	nlog.Core().Info("TLS certificate loaded from panel content (in-memory)")
	return nil
}

// ─── Mode: http / dns (ACME) ──────────────────────────────────────────────

func (m *Manager) startACME(ctx context.Context, dnsSolver *certmagic.DNS01Solver) error {
	// Idempotent for unchanged config: Reconfigure already tore down stale ACME state.
	if m.acmeStarted {
		return nil
	}

	if m.cfg.Domain == "" {
		return fmt.Errorf("cert.domain is required for ACME modes (http/dns)")
	}

	if err := os.MkdirAll(m.cfg.CertDir, 0o755); err != nil {
		return fmt.Errorf("create cert dir: %w", err)
	}

	cfg := m.cfg
	generation := m.generation.Add(1)
	storage := &privateStorage{Storage: &certmagic.FileStorage{Path: m.cfg.CertDir}, root: m.cfg.CertDir}

	var magic *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(_ certmagic.Certificate) (*certmagic.Config, error) {
			return magic, nil
		},
	})

	m.cache = cache
	initialized := false
	defer func() {
		if !initialized {
			m.tearDownACME()
		}
	}()
	magic = certmagic.New(cache, certmagic.Config{
		Storage: storage,
		OnEvent: func(evtCtx context.Context, event string, data map[string]any) error {
			// Only react to renewals; initial load happens explicitly after ObtainCertSync.
			if event == "cert_failed" && generation == m.generation.Load() {
				m.renewalStatus(resolveModeFor(cfg), "自动续期失败，请检查验证设置与节点日志")
			}
			if event != "cert_obtained" {
				return nil
			}
			if renewed, _ := data["renewal"].(bool); !renewed {
				return nil
			}
			issuerKey, _ := data["issuer"].(string)
			if issuerKey == "" {
				return nil
			}
			certPEM, keyPEM, err := readPEMFromStorage(evtCtx, storage, issuerKey, cfg.Domain)
			if err != nil {
				nlog.Core().Error("failed to reload cert after renewal", "error", err)
				return nil
			}
			m.publishRenewal(cfg, generation, certPEM, keyPEM)
			nlog.Core().Info("TLS certificate reloaded after renewal", "domain", cfg.Domain)
			return nil
		},
	})

	issuer := certmagic.ACMEIssuer{
		CA:    certmagic.LetsEncryptProductionCA,
		Email: m.cfg.Email,
	}

	if dnsSolver != nil {
		// DNS-01 mode: no HTTP port needed, supports wildcards.
		issuer.DNS01Solver = dnsSolver
		issuer.DisableHTTPChallenge = true
		issuer.DisableTLSALPNChallenge = true
	} else {
		// HTTP-01 mode.
		httpPort := m.cfg.HTTPPort
		if httpPort == 0 {
			httpPort = 80
		}
		issuer.AltHTTPPort = httpPort
		issuer.DisableTLSALPNChallenge = true
	}

	magic.Issuers = []certmagic.Issuer{
		certmagic.NewACMEIssuer(magic, issuer),
	}
	m.magic = magic

	// Derived ctx so Reconfigure can cancel ACME background goroutines.
	acmeCtx, acmeCancel := context.WithCancel(ctx)
	m.acmeCancel = acmeCancel

	if err := magic.ObtainCertSync(acmeCtx, m.cfg.Domain); err != nil {
		acmeCancel()
		return fmt.Errorf("obtain certificate: %w", err)
	}

	issuerKey := magic.Issuers[0].IssuerKey()
	if err := m.loadPEMFromStorage(acmeCtx, storage, issuerKey, m.cfg.Domain); err != nil {
		acmeCancel()
		return fmt.Errorf("load cert from storage: %w", err)
	}

	if err := magic.ManageAsync(acmeCtx, []string{m.cfg.Domain}); err != nil {
		acmeCancel()
		return fmt.Errorf("start cert manager: %w", err)
	}

	m.acmeCancel = acmeCancel
	m.acmeFingerprint = acmeFingerprint(m.cfg)
	m.acmeStarted = true
	initialized = true
	return nil
}

func validateKeyPair(certPEM, keyPEM []byte) error {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	return nil
}

// ─── DNS Provider Factory ──────────────────────────────────────────────────

// buildDNSSolver builds a certmagic DNS01Solver from the configured provider.
func (m *Manager) buildDNSSolver() (*certmagic.DNS01Solver, error) {
	provider, err := m.newDNSProvider()
	if err != nil {
		return nil, err
	}
	return &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: provider}}, nil
}

func (m *Manager) newDNSProvider() (certmagic.DNSProvider, error) {
	name := strings.TrimSpace(m.cfg.DNSProvider)
	if name == "" {
		return nil, fmt.Errorf("dns_provider is required for cert_mode=dns")
	}
	env := m.cfg.DNSEnv
	if env == nil {
		env = map[string]string{}
	}
	p, ok := dnsproviders.Get(name)
	if !ok {
		return nil, fmt.Errorf("unsupported dns_provider: %q (supported: %s)",
			name, strings.Join(dnsproviders.CanonicalNames(), ", "))
	}
	return p.Build(env)
}

// ─── Helpers ───────────────────────────────────────────────────────────────

// loadPEMFromStorage reads cert + key for domain from certmagic storage into
// the in-memory cache. Single entry point for both initial load and renewal refresh.
func (m *Manager) loadPEMFromStorage(ctx context.Context, storage certmagic.Storage, issuerKey, domain string) error {
	cert, key, err := readPEMFromStorage(ctx, storage, issuerKey, domain)
	if err != nil {
		return err
	}
	m.storePEM(cert, key)
	return nil
}

func readPEMFromStorage(ctx context.Context, storage certmagic.Storage, issuerKey, domain string) ([]byte, []byte, error) {
	if issuerKey == "" || domain == "" {
		return nil, nil, fmt.Errorf("issuerKey and domain are required")
	}
	certPEM, err := storage.Load(ctx, certmagic.StorageKeys.SiteCert(issuerKey, domain))
	if err != nil {
		return nil, nil, fmt.Errorf("load certificate: %w", err)
	}
	keyPEM, err := storage.Load(ctx, certmagic.StorageKeys.SitePrivateKey(issuerKey, domain))
	if err != nil {
		return nil, nil, fmt.Errorf("load private key: %w", err)
	}
	if err := validateCertificate(certPEM, keyPEM, domain); err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

func validateCertificate(certPEM, keyPEM []byte, domain string) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return fmt.Errorf("certificate is outside its validity period")
	}
	if domain != "" {
		return leaf.VerifyHostname(domain)
	}
	return nil
}

func (m *Manager) publishRenewal(cfg config.CertConfig, generation uint64, cert, key []byte) bool {
	m.materialMu.Lock()
	defer m.materialMu.Unlock()
	if generation != m.generation.Load() {
		return false
	}
	m.publishPEM(cfg, cert, key)
	m.renewed.Store(true)
	return true
}
