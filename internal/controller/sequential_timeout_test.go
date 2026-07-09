package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// targetAgent builds the successor persona's Agent with the given runTimeout.
func targetAgent(runTimeout string) *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack-writer", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{RunTimeout: runTimeout},
			},
		},
	}
}

func TestSequentialRunTimeout(t *testing.T) {
	tests := []struct {
		name           string
		edgeTimeout    string
		agentRunTimout string
		want           *time.Duration
	}{
		{
			name:        "edge timeout wins over the agent default",
			edgeTimeout: "5m", agentRunTimout: "30m",
			want: ptr.To(5 * time.Minute),
		},
		{
			name:        "edge without a timeout falls back to the agent default",
			edgeTimeout: "", agentRunTimout: "30m",
			want: ptr.To(30 * time.Minute),
		},
		{
			// A bad edge value must not silently become an unbounded run, nor zero.
			name:        "malformed edge timeout falls back to the agent default",
			edgeTimeout: "5 minutes", agentRunTimout: "30m",
			want: ptr.To(30 * time.Minute),
		},
		{
			name:        "zero edge timeout falls back to the agent default",
			edgeTimeout: "0s", agentRunTimout: "30m",
			want: ptr.To(30 * time.Minute),
		},
		{
			// Nil leaves the controller watchdog on its own flat default.
			name:        "neither set yields no persisted timeout",
			edgeTimeout: "", agentRunTimout: "",
			want: nil,
		},
		{
			name:        "edge timeout applies even when the agent has no default",
			edgeTimeout: "5m", agentRunTimout: "",
			want: ptr.To(5 * time.Minute),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := sympoziumv1alpha1.AgentConfigRelationship{
				Source: "writer", Target: "reviewer", Type: "sequential", Timeout: tt.edgeTimeout,
			}
			got := sequentialRunTimeout(rel, targetAgent(tt.agentRunTimout))

			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %s", *tt.want)
			}
			if got.Duration != *tt.want {
				t.Errorf("got %s, want %s", got.Duration, *tt.want)
			}
		})
	}
}
