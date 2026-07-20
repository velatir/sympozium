package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// PromptServerDeps groups the inputs needed to build an LLMProvider and run
// the prompt-server loop. It builds on the prompt-server shape described
// in docs/modes/sidecar-driven.md (PR #302) and the
// internal/ipc.PromptRequest types, with a per-run provider instance
// serialised via sync.Mutex (Prompt() mutates the provider's messages
// slice internally, so concurrent calls would race).
//
// The provider is built ONCE per run and serialized via sync.Mutex so
// Prompt() (which mutates the provider's internal messages slice) does
// not race against itself. Per-request provider construction was tried
// and rejected: with USE_CONTEXT=true, fresh-provider-per-request loses
// history across calls; with USE_CONTEXT=false, fresh-provider-per-request
// makes ResetContext pointless.
type PromptServerDeps struct {
	Ctx             context.Context
	ProviderName    string
	APIKey          string
	BaseURL         string
	Model           string
	SystemPrompt    string
	Task            string
	Tools           []ToolDef
	ProviderHeaders map[string]string
}

// runPromptServiceLoop keeps the agent-runner alive in
// prompt-server mode: a sidecar drives individual LLM calls via the
// /ipc/prompts IPC channel while the agent-runner answers them.
//
// Behaviour:
//   - /ipc/prompts/request-{id}.json: parse, call provider.Prompt, then
//     write /ipc/prompts/result-{id}.json with the answer. With
//     USE_CONTEXT=true the prompt is appended to the provider's running
//     history; with USE_CONTEXT=false each prompt is stateless.
//   - /ipc/context/clear-{id}.json: call provider.ResetContext. The next
//     request served by the same loop will start from a fresh history.
//     Fire-and-forget: no result file is written.
//   - /ipc/done (written by the sidecar in a finally block, after writing
//     /ipc/run-result.json): the loop exits. The caller is responsible
//     for surfacing the run-result on the AgentRun CR.
//
// Exits when /ipc/done is written, the context is cancelled, or a fatal
// provider error occurs. A non-nil error signals the agent-runner should
// write an error result and surface it on the AgentRun.
func runPromptServiceLoop(deps PromptServerDeps) error {
	promptsDir := "/ipc/prompts"
	contextsDir := "/ipc/context"
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", promptsDir, err)
	}
	if err := os.MkdirAll(contextsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", contextsDir, err)
	}

	useContext := true
	if v := os.Getenv("USE_CONTEXT"); v != "" {
		useContext = strings.EqualFold(v, "true") || v == "1"
	}
	log.Printf("prompt service starting — useContext=%v provider=%s model=%s",
		useContext, deps.ProviderName, deps.Model)

	p, cleanup, err := buildLLMProvider(deps)
	if err != nil {
		return fmt.Errorf("build provider: %w", err)
	}
	defer cleanup()

	var mu sync.Mutex

	processRequest := func(req ipc.PromptRequest) {
		mu.Lock()
		defer mu.Unlock()

		log.Printf("prompt service: requestId=%s schema?=%v promptLen=%d",
			req.RequestID, len(req.Schema) > 0, len(req.Prompt))

		text, parsed, inTok, outTok, promptErr := p.Prompt(deps.Ctx, req.Prompt, useContext, req.Schema)
		result := ipc.PromptResult{
			RequestID: req.RequestID,
			Status:    "success",
			Content:   text,
			Parsed:    parsed,
		}
		result.Metrics.InputTokens = inTok
		result.Metrics.OutputTokens = outTok
		if promptErr != nil {
			result.Status = "error"
			result.Error = promptErr.Error()
		}
		writePromptResult(promptsDir, req.RequestID, result)
		log.Printf("prompt service: requestId=%s status=%s inTok=%d outTok=%d",
			req.RequestID, result.Status, result.Metrics.InputTokens, result.Metrics.OutputTokens)
	}

	processContextClear := func(req ipc.ClearContextRequest) {
		mu.Lock()
		defer mu.Unlock()
		p.ResetContext()
		log.Printf("prompt service: context cleared (requestId=%s reason=%q)", req.RequestID, req.Reason)
	}

	knownRequests := map[string]bool{}
	knownClears := map[string]bool{}

	poll := time.NewTicker(150 * time.Millisecond)
	defer poll.Stop()

	doneCh := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-deps.Ctx.Done():
				return
			case <-t.C:
				if _, err := os.Stat("/ipc/done"); err == nil {
					select {
					case doneCh <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()

	for {
		select {
		case <-deps.Ctx.Done():
			return deps.Ctx.Err()
		case <-doneCh:
			log.Println("prompt service: /ipc/done observed, exiting")
			return nil
		case <-poll.C:
			entries, err := os.ReadDir(promptsDir)
			if err == nil {
				for _, e := range entries {
					name := e.Name()
					if !strings.HasPrefix(name, "request-") || knownRequests[name] {
						continue
					}
					knownRequests[name] = true
					path := filepath.Join(promptsDir, name)
					data, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					var req ipc.PromptRequest
					if err := json.Unmarshal(data, &req); err != nil {
						log.Printf("prompt service: dropping invalid request %s: %v", name, err)
						continue
					}
					if req.RequestID == "" {
						log.Printf("prompt service: dropping request %s with empty RequestID", name)
						continue
					}
					processRequest(req)
				}
			}

			entries, err = os.ReadDir(contextsDir)
			if err == nil {
				for _, e := range entries {
					name := e.Name()
					if !strings.HasPrefix(name, "clear-") || knownClears[name] {
						continue
					}
					knownClears[name] = true
					path := filepath.Join(contextsDir, name)
					data, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					var req ipc.ClearContextRequest
					if err := json.Unmarshal(data, &req); err != nil {
						log.Printf("prompt service: dropping invalid clear request %s: %v", name, err)
						continue
					}
					processContextClear(req)
				}
			}
		}
	}
}

// writePromptResult atomically writes a PromptResult JSON to
// /ipc/prompts/result-{RequestID}.json so the sidecar (which fsnotify-watches
// the directory) sees a complete file with stable bytes.
func writePromptResult(dir, requestID string, result ipc.PromptResult) {
	if requestID == "" {
		return
	}
	path := filepath.Join(dir, "result-"+requestID+".json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("prompt service: failed to marshal result: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("prompt service: failed to write %s: %v", path, err)
	}
}

// buildLLMProvider constructs the per-provider instance for the prompt
// service loop. Mirrors the existing buildProvider dispatch in main() so
// the prompt-server path uses the exact same provider types.
func buildLLMProvider(deps PromptServerDeps) (LLMProvider, func(), error) {
	switch strings.ToLower(deps.ProviderName) {
	case "anthropic":
		return newAnthropicProvider(deps.APIKey, deps.BaseURL, deps.Model, deps.SystemPrompt, deps.Task, deps.Tools, deps.ProviderHeaders), func() {}, nil
	case "bedrock":
		p, err := newBedrockProvider(deps.Ctx, deps.Model, deps.SystemPrompt, deps.Task, deps.Tools)
		if err != nil {
			return nil, nil, err
		}
		return p, func() {}, nil
	default:
		p, err := newOpenAIProvider(deps.ProviderName, deps.APIKey, deps.BaseURL, deps.Model, deps.SystemPrompt, deps.Task, deps.Tools, deps.ProviderHeaders)
		if err != nil {
			return nil, nil, err
		}
		return p, func() {}, nil
	}
}
