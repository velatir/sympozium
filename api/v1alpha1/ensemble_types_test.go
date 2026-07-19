package v1alpha1

import (
	"testing"
	"time"
)

func TestAgentConfigRelationship_ParseTimeout(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantNil   bool
		wantValue time.Duration
	}{
		{name: "minutes", value: "15m", wantValue: 15 * time.Minute},
		{name: "hours", value: "1h", wantValue: time.Hour},
		{name: "compound", value: "1h30m", wantValue: 90 * time.Minute},
		{name: "unset", value: "", wantNil: true},
		{name: "malformed", value: "15 minutes", wantNil: true},
		{name: "bare number", value: "15", wantNil: true},
		// Zero and negative must not be honored: an edge timeout of 0 would
		// expire the delegation the instant it is armed.
		{name: "zero", value: "0s", wantNil: true},
		{name: "negative", value: "-5m", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AgentConfigRelationship{Timeout: tt.value}.ParseTimeout()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ParseTimeout(%q) = %v, want nil", tt.value, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ParseTimeout(%q) = nil, want %s", tt.value, tt.wantValue)
			}
			if got.Duration != tt.wantValue {
				t.Fatalf("ParseTimeout(%q) = %s, want %s", tt.value, got.Duration, tt.wantValue)
			}
		})
	}
}
