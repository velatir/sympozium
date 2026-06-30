package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channel "github.com/sympozium-ai/sympozium/internal/channel"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// TestHandleCompleted_Routing covers the skipped run staying silent (no channel
// reply) alongside the positive control that a normal result is routed back —
// the control guards the skipped assertion from passing vacuously.
func TestHandleCompleted_Routing(t *testing.T) {
	tests := []struct {
		name          string
		result        agentResult
		wantPublished int
	}{
		{
			name:          "skipped run stays silent",
			result:        agentResult{Status: ipc.ResultStatusSkipped, Response: "no new items in queue"},
			wantPublished: 0,
		},
		{
			name:          "success routes reply",
			result:        agentResult{Status: "success", Response: "here you go"},
			wantPublished: 1,
		},
	}

	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &sympoziumv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "chan-run",
					Namespace: "default",
					Labels:    map[string]string{"sympozium.ai/source": "channel"},
					Annotations: map[string]string{
						"sympozium.ai/reply-channel": "telegram",
						"sympozium.ai/reply-chat-id": "456",
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			bus := &recordingEventBus{}
			cr := &ChannelRouter{Client: cl, EventBus: bus, Log: logr.Discard()}

			event, err := eventbus.NewEvent(eventbus.TopicAgentRunCompleted, map[string]string{
				"agentRunID":   "chan-run",
				"instanceName": "demo",
			}, tt.result)
			if err != nil {
				t.Fatalf("build event: %v", err)
			}

			cr.handleCompleted(context.Background(), event)

			if len(bus.published) != tt.wantPublished {
				t.Fatalf("published events = %d, want %d", len(bus.published), tt.wantPublished)
			}
			if tt.wantPublished == 1 && bus.published[0].Topic != eventbus.TopicChannelMessageSend {
				t.Fatalf("published topic = %q, want %q", bus.published[0].Topic, eventbus.TopicChannelMessageSend)
			}
		})
	}
}

func TestCheckChannelAccess(t *testing.T) {
	tests := []struct {
		name        string
		channels    []sympoziumv1alpha1.ChannelSpec
		msg         channel.InboundMessage
		wantAllowed bool
		wantDeny    string
	}{
		{
			name:        "no access control configured",
			channels:    []sympoziumv1alpha1.ChannelSpec{{Type: "telegram"}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name:        "channel type not in instance spec",
			channels:    []sympoziumv1alpha1.ChannelSpec{{Type: "slack"}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "allowed sender in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"123", "789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "allowed sender not in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"789", "012"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "denied sender in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					DeniedSenders: []string{"123"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "denied sender not in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					DeniedSenders: []string{"789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "sender in both allow and deny lists - deny wins",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"123"},
					DeniedSenders:  []string{"123"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "allowed chat in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"456", "789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "allowed chat not in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "allowed chat passes but denied sender blocks",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats:  []string{"456"},
					DeniedSenders: []string{"123"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "deny message returned when set",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"789"},
					DenyMessage:    "You are not authorized.",
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
			wantDeny:    "You are not authorized.",
		},
		{
			name: "deny message empty when not set",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
			wantDeny:    "",
		},
		{
			name: "discord channel ID routing via AllowedChats - denied",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "discord",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"1234567890123456789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "discord", SenderID: "user1", ChatID: "9999999999999999999"},
			wantAllowed: false,
		},
		{
			name: "discord channel ID routing via AllowedChats - allowed",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "discord",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"1234567890123456789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "discord", SenderID: "user1", ChatID: "1234567890123456789"},
			wantAllowed: true,
		},
		{
			name: "all checks pass",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"123"},
					AllowedChats:   []string{"456"},
					DeniedSenders:  []string{"999"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &sympoziumv1alpha1.Agent{
				Spec: sympoziumv1alpha1.AgentSpec{
					Channels: tt.channels,
				},
			}
			allowed, denyMsg := checkChannelAccess(inst, &tt.msg)
			if allowed != tt.wantAllowed {
				t.Errorf("checkChannelAccess() allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			if denyMsg != tt.wantDeny {
				t.Errorf("checkChannelAccess() denyMsg = %q, want %q", denyMsg, tt.wantDeny)
			}
		})
	}
}
