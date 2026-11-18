package dnsproviders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

func TestGoogleDNSTXTChangePreservesOtherChallenges(t *testing.T) {
	changes := make(chan dns.Change, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns/v1/projects/test/managedZones":
			_, _ = w.Write([]byte(`{"managedZones":[{"name":"private","dnsName":"example.test.","visibility":"private"},{"name":"public","dnsName":"example.test.","visibility":"public"}]}`))
		case "/dns/v1/projects/test/managedZones/public/rrsets/_acme-challenge.example.test./TXT":
			values := []string{`"other"`}
			if len(changes) > 0 {
				values = append(values, `"challenge"`)
			}
			_ = json.NewEncoder(w).Encode(dns.ResourceRecordSet{Name: "_acme-challenge.example.test.", Type: "TXT", Ttl: 60, Rrdatas: values})
		case "/dns/v1/projects/test/managedZones/public/changes":
			var change dns.Change
			if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
				http.Error(w, "invalid change", http.StatusBadRequest)
				return
			}
			changes <- change
			_, _ = w.Write([]byte(`{"status":"done"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	api, err := dns.NewService(ctx, option.WithHTTPClient(server.Client()), option.WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	provider := &googleDNS{project: "test", service: api}
	records := []libdns.Record{libdns.TXT{Name: "_acme-challenge", Text: "challenge", TTL: 60 * time.Second}}
	if _, err := provider.AppendRecords(ctx, "example.test.", records); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DeleteRecords(ctx, "example.test.", records); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatal("应分别提交新增和删除验证记录")
	}
	for _, expected := range [][]string{{`"other"`, `"challenge"`}, {`"other"`}} {
		change := <-changes
		if len(change.Additions) != 1 || len(change.Deletions) != 1 || !reflect.DeepEqual(change.Additions[0].Rrdatas, expected) {
			t.Fatalf("TXT 更新没有保留同域其他验证记录: %+v", change)
		}
	}
}
