package nlog

import (
	"strings"
	"testing"
)

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
