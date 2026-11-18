package dnsproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/googleclouddns"
	"github.com/libdns/libdns"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func init() {
	Register(&Provider{
		Names:   []string{"googleclouddns", "gcp", "gcloud"},
		EnvVars: []string{"GCE_PROJECT", "GCE_SERVICE_ACCOUNT_JSON", "GCE_SERVICE_ACCOUNT_FILE"},
		Build: func(env map[string]string) (certmagic.DNSProvider, error) {
			project := firstOf(env, "GCE_PROJECT", "GOOGLE_PROJECT_ID", "GCP_PROJECT")
			if project == "" {
				return nil, missingEnvErr("googleclouddns", "GCE_PROJECT")
			}
			if raw := firstOf(env, "GCE_SERVICE_ACCOUNT_JSON"); raw != "" {
				return newGoogleDNS(project, raw)
			}
			file := firstOf(env, "GCE_SERVICE_ACCOUNT_FILE", "GOOGLE_APPLICATION_CREDENTIALS")
			if strings.HasPrefix(file, "{") {
				return newGoogleDNS(project, file)
			}
			// 上游字段只接受文件路径。本机配置也可使用默认凭据链。
			return &googleclouddns.Provider{
				Project:            project,
				ServiceAccountJSON: file,
			}, nil
		},
	})
}

type googleDNS struct {
	project string
	service *dns.Service
	mu      sync.Mutex
}

func newGoogleDNS(project, raw string) (*googleDNS, error) {
	var credentials struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
		TokenURI     string `json:"token_uri"`
	}
	if json.Unmarshal([]byte(raw), &credentials) != nil || credentials.Type != "service_account" ||
		credentials.ClientEmail == "" || credentials.PrivateKey == "" {
		return nil, fmt.Errorf("GCP DNS 需要有效的服务账户 JSON")
	}
	// 只传递服务账户需要的字段，认证请求使用固定的 Google 接口。
	credentials.TokenURI = "https://oauth2.googleapis.com/token"
	data, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	service, err := dns.NewService(context.Background(),
		option.WithScopes(dns.NdevClouddnsReadwriteScope), option.WithAuthCredentialsJSON(option.ServiceAccount, data))
	if err != nil {
		return nil, fmt.Errorf("无法初始化 GCP DNS 服务账户")
	}
	return &googleDNS{project: project, service: service}, nil
}

func (p *googleDNS) AppendRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	return p.change(ctx, zone, records, true)
}

func (p *googleDNS) DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	return p.change(ctx, zone, records, false)
}

func (p *googleDNS) change(ctx context.Context, zone string, records []libdns.Record, appendRecord bool) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var managed string
	fqdn := strings.TrimSuffix(zone, ".") + "."
	err := p.service.ManagedZones.List(p.project).DnsName(fqdn).Pages(ctx, func(page *dns.ManagedZonesListResponse) error {
		for _, zone := range page.ManagedZones {
			if zone.DnsName == fqdn && zone.Visibility == "public" && managed == "" {
				managed = zone.Name
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if managed == "" {
		return nil, fmt.Errorf("GCP DNS 找不到对应的公网区域")
	}
	var changed []libdns.Record
	for _, record := range records {
		rr := record.RR()
		if rr.Type != "TXT" {
			return changed, fmt.Errorf("GCP DNS 内联凭据仅用于证书 TXT 验证")
		}
		name := libdns.AbsoluteName(rr.Name, fqdn)
		name = strings.TrimSuffix(name, ".") + "."
		sets := p.service.ResourceRecordSets
		before, err := sets.Get(p.project, managed, name, rr.Type).Context(ctx).Do()
		if apiErr, ok := err.(*googleapi.Error); ok && apiErr.Code == http.StatusNotFound {
			before, err = nil, nil
		}
		if err != nil {
			return changed, err
		}
		value := strconv.Quote(rr.Data)
		after := &dns.ResourceRecordSet{Name: name, Type: rr.Type, Ttl: max(1, int64(rr.TTL/time.Second))}
		if before != nil {
			after.Ttl = before.Ttl
			after.Rrdatas = slices.Clone(before.Rrdatas)
		}
		index := slices.Index(after.Rrdatas, value)
		if appendRecord && index < 0 {
			after.Rrdatas = append(after.Rrdatas, value)
		} else if !appendRecord && index >= 0 {
			after.Rrdatas = slices.Delete(after.Rrdatas, index, index+1)
		} else {
			continue
		}
		change := &dns.Change{}
		if before != nil {
			change.Deletions = []*dns.ResourceRecordSet{before}
		}
		if len(after.Rrdatas) > 0 {
			change.Additions = []*dns.ResourceRecordSet{after}
		}
		if _, err := p.service.Changes.Create(p.project, managed, change).Context(ctx).Do(); err != nil {
			return changed, err
		}
		changed = append(changed, record)
	}
	return changed, nil
}
