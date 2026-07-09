package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func draSlice(name, node, driver string, devices ...resourceapi.Device) *resourceapi.ResourceSlice {
	return &resourceapi.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: resourceapi.ResourceSliceSpec{
			Driver:   driver,
			NodeName: ptr.To(node),
			Pool:     resourceapi.ResourcePool{Name: node, ResourceSliceCount: 1},
			Devices:  devices,
		},
	}
}

func attrs(kv map[string]any) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	out := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}
	for k, v := range kv {
		switch t := v.(type) {
		case string:
			out[resourceapi.QualifiedName(k)] = resourceapi.DeviceAttribute{StringValue: ptr.To(t)}
		case bool:
			out[resourceapi.QualifiedName(k)] = resourceapi.DeviceAttribute{BoolValue: ptr.To(t)}
		case int:
			out[resourceapi.QualifiedName(k)] = resourceapi.DeviceAttribute{IntValue: ptr.To(int64(t))}
		}
	}
	return out
}

func TestListDRANodes(t *testing.T) {
	gpu := resourceapi.Device{
		Name: "gpu-0000-c3-00-0",
		Attributes: attrs(map[string]any{
			"kind": "gpu", "model": "AMD Radeon RX 7900 XTX", "vendor": "amd",
			"healthy": true, "memoryBandwidthGBs": 960, "computeTFLOPS": 61,
		}),
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {Value: *resource.NewQuantity(24*1024*1024*1024, resource.BinarySI)},
		},
	}
	nic := resourceapi.Device{
		Name: "nic-0000-41-00-0",
		Attributes: attrs(map[string]any{
			"kind": "nic", "vendor": "mellanox", "healthy": true,
			"linkLayer": "ethernet", "rateGbps": 100,
		}),
	}
	foreign := resourceapi.Device{Name: "other", Attributes: attrs(map[string]any{"kind": "gpu"})}

	kube := kubefake.NewClientset(
		draSlice("node-a-slice", "node-a", "llmfit.ai", gpu, nic),
		draSlice("node-b-slice", "node-b", "gpu.nvidia.com", foreign),
	)
	s := NewServer(nil, nil, kube, logr.Discard())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dra/nodes", nil)
	rec := httptest.NewRecorder()
	s.listDRANodes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp draNodesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !resp.Available {
		t.Error("available should be true when the API serves slices")
	}
	// Foreign-driver slices must not surface.
	if resp.Total != 1 || len(resp.Nodes) != 1 || resp.Nodes[0].NodeName != "node-a" {
		t.Fatalf("nodes = %+v, want only node-a", resp.Nodes)
	}
	devs := resp.Nodes[0].Devices
	if len(devs) != 2 {
		t.Fatalf("devices = %+v, want gpu + nic", devs)
	}
	// Sorted by name: gpu-… before nic-….
	g, n := devs[0], devs[1]
	if g.Kind != "gpu" || g.Model != "AMD Radeon RX 7900 XTX" || g.MemoryGi != 24 ||
		g.MemoryBandwidthGBs != 960 || g.ComputeTFLOPS != 61 || !g.Healthy {
		t.Errorf("gpu device mismatch: %+v", g)
	}
	if n.Kind != "nic" || n.LinkLayer != "ethernet" || n.RateGbps != 100 || n.MemoryGi != 0 {
		t.Errorf("nic device mismatch: %+v", n)
	}
}

func TestListDRANodesEmptyCluster(t *testing.T) {
	s := NewServer(nil, nil, kubefake.NewClientset(), logr.Discard())
	rec := httptest.NewRecorder()
	s.listDRANodes(rec, httptest.NewRequest(http.MethodGet, "/api/v1/dra/nodes", nil))
	var resp draNodesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !resp.Available || resp.Total != 0 || len(resp.Nodes) != 0 {
		t.Errorf("empty cluster: %+v, want available with zero nodes", resp)
	}
}
