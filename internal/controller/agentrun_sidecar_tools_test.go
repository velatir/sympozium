package controller

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

func TestSidecarsHaveTools(t *testing.T) {
	none := []resolvedSidecar{
		{skillPackName: "a", sidecar: sympoziumv1alpha1.SkillSidecar{}},
	}
	if sidecarsHaveTools(none) {
		t.Error("expected false when no sidecar declares tools")
	}

	some := []resolvedSidecar{
		{skillPackName: "a", sidecar: sympoziumv1alpha1.SkillSidecar{}},
		{skillPackName: "b", sidecar: sympoziumv1alpha1.SkillSidecar{
			Tools: []sympoziumv1alpha1.SidecarTool{{Name: "t", Exec: []string{"true"}}},
		}},
	}
	if !sidecarsHaveTools(some) {
		t.Error("expected true when a sidecar declares tools")
	}
}

func TestBuildSidecarToolsManifest(t *testing.T) {
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"serviceIdentifier":{"type":"string"}}}`)}

	sidecars := []resolvedSidecar{
		{
			skillPackName: "Service-Discovery", // mixed case to verify normalization
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{
						Name:           "sd_evaluate",
						Description:    "Evaluate changes",
						Exec:           []string{"node", "/app/dist/cli.js"},
						Subcommand:     "evaluate-changes",
						InputMode:      "stdin",
						PositionalArgs: []string{"serviceIdentifier"},
						Parameters:     &params,
					},
					{
						Name: "sd_list", // no InputMode / no Parameters -> defaults applied
						Exec: []string{"node", "/app/dist/cli.js"},
					},
				},
			},
		},
	}

	out, err := buildSidecarToolsManifest(sidecars)
	if err != nil {
		t.Fatalf("buildSidecarToolsManifest: %v", err)
	}

	var manifest struct {
		Tools []struct {
			Name           string          `json:"name"`
			Target         string          `json:"target"`
			Exec           []string        `json:"exec"`
			Subcommand     string          `json:"subcommand"`
			InputMode      string          `json:"inputMode"`
			PositionalArgs []string        `json:"positionalArgs"`
			Parameters     json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out), &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if len(manifest.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(manifest.Tools))
	}

	first := manifest.Tools[0]
	// Target is controller-derived from the SkillPack name (normalized), never
	// taken from the manifest input — this is the trust boundary.
	if first.Target != "service-discovery" {
		t.Errorf("target = %q, want service-discovery (normalized from SkillPack name)", first.Target)
	}
	if first.InputMode != "stdin" {
		t.Errorf("inputMode = %q, want stdin", first.InputMode)
	}
	if len(first.Exec) != 2 || first.Exec[0] != "node" {
		t.Errorf("exec = %v, want [node /app/dist/cli.js]", first.Exec)
	}

	second := manifest.Tools[1]
	if second.InputMode != "args" {
		t.Errorf("default inputMode = %q, want args", second.InputMode)
	}
	// A tool with no Parameters gets a default empty object schema, never null.
	var schema map[string]any
	if err := json.Unmarshal(second.Parameters, &schema); err != nil {
		t.Fatalf("default parameters not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("default parameters = %v, want an object schema", schema)
	}
}

func TestBuildSidecarToolsManifest_Empty(t *testing.T) {
	out, err := buildSidecarToolsManifest(nil)
	if err != nil {
		t.Fatalf("buildSidecarToolsManifest(nil): %v", err)
	}
	if out != `{"tools":[]}` {
		t.Errorf("empty manifest = %q, want an empty tools array", out)
	}
}

// TestSidecarTools_PodSpecWiring verifies that when a resolved sidecar declares
// native tools, the agent pod gets the read-only sidecar-tools volume, the agent
// container mounts it read-only, and the manifest-path env is set. With no tools
// declared, none of that wiring appears.
func TestSidecarTools_PodSpecWiring(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	withTools := []resolvedSidecar{{
		skillPackName: "svc",
		sidecar: sympoziumv1alpha1.SkillSidecar{
			Image: "example/sidecar:latest",
			Tools: []sympoziumv1alpha1.SidecarTool{{Name: "svc_eval", Exec: []string{"true"}}},
		},
	}}

	// Volume present and references the controller-written ConfigMap.
	vols := r.buildVolumes(run, false, withTools, nil)
	var vol *corev1.Volume
	for i := range vols {
		if vols[i].Name == "sidecar-tools" {
			vol = &vols[i]
		}
	}
	if vol == nil {
		t.Fatal("expected a sidecar-tools volume")
	}
	if vol.ConfigMap == nil || vol.ConfigMap.Name != run.Name+"-sidecar-tools" {
		t.Errorf("sidecar-tools volume should source ConfigMap %q, got %+v", run.Name+"-sidecar-tools", vol.VolumeSource)
	}

	// Agent container gets the read-only mount + manifest-path env.
	cs, _ := r.buildContainers(run, false, nil, withTools, nil, nil, nil)
	agent := cs[0]
	var mountOK bool
	for _, m := range agent.VolumeMounts {
		if m.Name == "sidecar-tools" {
			if m.MountPath != "/config/sidecar-tools" || !m.ReadOnly {
				t.Errorf("sidecar-tools mount must be read-only at /config/sidecar-tools, got %+v", m)
			}
			mountOK = true
		}
	}
	if !mountOK {
		t.Error("agent container is missing the sidecar-tools mount")
	}
	var envOK bool
	for _, e := range agent.Env {
		if e.Name == "SIDECAR_TOOLS_MANIFEST_PATH" {
			if e.Value != "/config/sidecar-tools/sidecar-tools.json" {
				t.Errorf("SIDECAR_TOOLS_MANIFEST_PATH = %q", e.Value)
			}
			envOK = true
		}
	}
	if !envOK {
		t.Error("agent container is missing SIDECAR_TOOLS_MANIFEST_PATH env")
	}

	// No tools -> no wiring.
	noTools := []resolvedSidecar{{skillPackName: "svc", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "x"}}}
	for _, v := range r.buildVolumes(run, false, noTools, nil) {
		if v.Name == "sidecar-tools" {
			t.Error("sidecar-tools volume must not appear when no tools are declared")
		}
	}
}

// TestBuildContainers_ObjectTaskSetsAgentMode: when spec.task is
// object-form with mode=sidecar-driven, the agent container must receive
// AGENT_MODE=prompt-server so the agent-runner skips the main loop and
// serves /ipc/prompts/ instead.
func TestBuildContainers_ObjectTaskSetsAgentMode(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Task = &sympoziumv1alpha1.TaskSpec{
		Mode: "sidecar-driven",
		Tool: "collector_run",
		Parameters: map[string]string{
			"services": `["chatgpt"]`,
		},
	}

	cs, _ := r.buildContainers(run, false, nil, nil, nil, nil, nil)
	agent := cs[0]

	var sawAgentMode bool
	for _, e := range agent.Env {
		if e.Name == "AGENT_MODE" && e.Value == "prompt-server" {
			sawAgentMode = true
		}
		if e.Name == "TASK" {
			t.Errorf("object-form task must NOT set TASK env (was %q)", e.Value)
		}
	}
	if !sawAgentMode {
		t.Error("expected AGENT_MODE=prompt-server on agent container env")
	}
}

// TestBuildContainers_ObjectTaskSetsUseContext: the AgentRun's
// UseContext toggle is propagated to the agent container as USE_CONTEXT.
// Default on nil is true.
func TestBuildContainers_ObjectTaskSetsUseContext(t *testing.T) {
	r := &AgentRunReconciler{}

	cases := []struct {
		name    string
		set     *bool
		wantEnv string
	}{
		{"nil defaults to true", nil, "true"},
		{"explicit true", ptrBool(true), "true"},
		{"explicit false", ptrBool(false), "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := newTestRun()
			run.Spec.UseContext = tc.set
			cs, _ := r.buildContainers(run, false, nil, nil, nil, nil, nil)
			var saw bool
			for _, e := range cs[0].Env {
				if e.Name == "USE_CONTEXT" && e.Value == tc.wantEnv {
					saw = true
				}
			}
			if !saw {
				t.Errorf("expected USE_CONTEXT=%s, got env=%v", tc.wantEnv, cs[0].Env)
			}
		})
	}
}

// TestBuildContainers_StringTaskSetsTASK (regression guard): string-form
// tasks still set TASK env (Path A: prompt goes to the LLM via TASK).
func TestBuildContainers_StringTaskSetsTASK(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	const prompt = "Run a content collection sweep."
	run.Spec.Task = sympoziumv1alpha1.NewStringTask(prompt)

	cs, _ := r.buildContainers(run, false, nil, nil, nil, nil, nil)
	agent := cs[0]

	var sawTask bool
	for _, e := range agent.Env {
		if e.Name == "TASK" && e.Value == prompt {
			sawTask = true
		}
		if e.Name == "AGENT_MODE" {
			t.Errorf("string-form task must NOT set AGENT_MODE (was %q)", e.Value)
		}
	}
	if !sawTask {
		t.Errorf("expected TASK=%q on agent container env", prompt)
	}
}

// TestBuildContainers_SidecarAdjustmentApplied: SidecarAdjustments
// from the TaskModeHandler dispatch override the sidecar command and append
// extra env. The skill loop in buildContainers must read the adjustments map
// keyed by SkillPackName.
func TestBuildContainers_SidecarAdjustmentApplied(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	sidecars := []resolvedSidecar{{
		skillPackName: "sd-collector",
		sidecar: sympoziumv1alpha1.SkillSidecar{
			Image:   "example/sidecar:latest",
			Command: []string{"echo", "default"},
		},
	}}
	adjustments := []taskmodes.SidecarAdjustment{
		{
			SkillPackName:   "sd-collector",
			OverrideCommand: []string{"node", "/app/cli.js", "primary"},
			AddEnv: []corev1.EnvVar{
				{Name: "SYMPOZIUM_RUN_CONFIG_JSON", Value: `{"services":["chatgpt"]}`},
			},
		},
	}

	cs, _ := r.buildContainers(run, false, nil, sidecars, nil, nil, adjustments)

	var found bool
	for _, c := range cs {
		if c.Name != "skill-sd-collector" {
			continue
		}
		found = true
		if !equalSlice(c.Command, []string{"node", "/app/cli.js", "primary"}) {
			t.Errorf("sidecar Command = %v, want override", c.Command)
		}
		var sawConfig bool
		for _, e := range c.Env {
			if e.Name == "SYMPOZIUM_RUN_CONFIG_JSON" && e.Value == `{"services":["chatgpt"]}` {
				sawConfig = true
			}
		}
		if !sawConfig {
			t.Errorf("sidecar env missing SYMPOZIUM_RUN_CONFIG_JSON: %v", c.Env)
		}
	}
	if !found {
		t.Fatal("skill-sd-collector sidecar container not found")
	}
}

func ptrBool(b bool) *bool { return &b }

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
