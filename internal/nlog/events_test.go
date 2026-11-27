package nlog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizePreservesDiagnosticContent(t *testing.T) {
	for _, tc := range []struct{ name, message string }{
		{"address", "dial https://example.invalid/api?token=fixture-value: connection refused"},
		{"identity", "uuid=00000000-0000-0000-0000-000000000000"},
		{"long_identifier", strings.Repeat("diagnostic", 12)},
		{"credentials", "Authorization: Basic fixture-value\npassword=fixture-value cookie=fixture-value"},
		{"config", "failed {\"config\":{\"token\":\"fixture-value\"}}\n[stacktrace] frame"},
		{"pem", "-----BEGIN PRIVATE KEY-----\nfixture-only\n-----END PRIVATE KEY-----"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.message); got != tc.message {
				t.Fatal("diagnostic content changed")
			}
		})
	}
}

func TestSanitizeCleansControlsAndBoundsUTF8(t *testing.T) {
	if got := Sanitize("\x1b[31m错误\x1b[0m\x00\n\t下一行"); got != "错误\n\t下一行" {
		t.Fatal("control character cleanup changed")
	}
	got := Sanitize(strings.Repeat("字", 1000))
	if len(got) > 1600 || !utf8.ValidString(got) || len(got) == 0 {
		t.Fatal("invalid bounded UTF-8 diagnostic")
	}
}

func BenchmarkPublishInfoFiltering(b *testing.B) {
	for _, filtered := range []bool{false, true} {
		name := "unfiltered"
		if filtered {
			name = "filtered"
		}
		b.Run(name, func(b *testing.B) {
			ConfigureLogPolicies(nil)
			scope := b.Name()
			var accept func(Event) bool
			if filtered {
				accept = func(event Event) bool { return event.Level != "info" }
			}
			stop := SubscribeFiltered(scope, accept, func(Event) {})
			b.Cleanup(stop)
			message := strings.Repeat("ordinary connection context ", 4)
			event := Event{Source: "agent", Level: "info", Message: message, OccurredAt: 1}
			b.ReportAllocs()
			for b.Loop() {
				Publish(scope, event)
			}
		})
	}
}
