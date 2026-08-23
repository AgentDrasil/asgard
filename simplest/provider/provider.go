// Package provider implements the two supported wire protocols as
// types.Provider implementations: OpenAI-compatible chat completions and
// Google Generative AI (Gemini), both over server-sent events using stdlib
// net/http only.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/types"
)

const eventBuffer = 64

// CalculateCost fills u.Cost from the model's $/MTok rates.
func CalculateCost(m *types.Model, u *types.Usage) {
	if m == nil || u == nil {
		return
	}
	u.Cost.Input = m.Cost.Input / 1e6 * float64(u.Input)
	u.Cost.Output = m.Cost.Output / 1e6 * float64(u.Output)
	u.Cost.CacheRead = m.Cost.CacheRead / 1e6 * float64(u.CacheRead)
	u.Cost.CacheWrite = m.Cost.CacheWrite / 1e6 * float64(u.CacheWrite)
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite
}

// postSSE issues a POST expecting an SSE response. Non-2xx responses are
// converted to errors carrying up to 4000 chars of the body, mirroring pi's
// normalizeProviderError formatting ("<status>: <body>").
func postSSE(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		snippet := string(bytes.TrimSpace(b))
		if len(snippet) > 4000 {
			snippet = snippet[:4000]
		}
		if snippet == "" {
			return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%d: %s", resp.StatusCode, snippet)
	}
	return resp, nil
}

// scanSSE calls fn for each "data:" payload line in the SSE stream.
func scanSSE(resp *http.Response, fn func(payload string) error) error {
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" {
			continue
		}
		if err := fn(payload); err != nil {
			return err
		}
	}
	return sc.Err()
}

// streamContext reports whether ctx is still alive; used to distinguish
// aborts from errors at stream end.
func streamAborted(ctx context.Context) bool {
	return ctx.Err() != nil
}

// emitter builds the streaming AssistantMessage and emits protocol events.
// It tracks the currently open text/thinking block so block switches emit
// matching *_end events, mirroring pi's stream assembly.
type emitter struct {
	ch     chan types.AssistantMessageEvent
	out    *types.AssistantMessage
	open   int    // index of the open block; -1 when none
	openKd string // "text" | "thinking" | ""
}

func newEmitter(ch chan types.AssistantMessageEvent) *emitter {
	return &emitter{ch: ch, out: &types.AssistantMessage{}, open: -1}
}

func (em *emitter) send(ev types.AssistantMessageEvent) { em.ch <- ev }

func (em *emitter) partial(kind types.AssistantMessageEventKind, idx int, delta, content string, tc *types.ToolCall) {
	em.send(types.Partial{
		Kind:         kind,
		ContentIndex: idx,
		Delta:        delta,
		Content:      content,
		ToolCall:     tc,
		Partial:      em.out,
	})
}

func (em *emitter) start(api, provider, model string) {
	em.out.API = api
	em.out.Provider = provider
	em.out.Model = model
	em.partial(types.EvStart, 0, "", "", nil)
}

// closeOpen terminates the currently open text/thinking block, if any.
func (em *emitter) closeOpen() {
	if em.open < 0 {
		return
	}
	idx := em.open
	switch b := em.out.Content[idx].(type) {
	case types.TextContent:
		em.partial(types.EvTextEnd, idx, "", b.Text, nil)
	case types.ThinkingContent:
		em.partial(types.EvThinkingEnd, idx, "", b.Thinking, nil)
	}
	em.open = -1
	em.openKd = ""
}

func (em *emitter) textDelta(delta string) {
	if em.open < 0 || em.openKd != "text" {
		em.closeOpen()
		em.out.Content = append(em.out.Content, types.TextContent{Type: types.TypeText})
		em.open = len(em.out.Content) - 1
		em.openKd = "text"
		em.partial(types.EvTextStart, em.open, "", "", nil)
	}
	b := em.out.Content[em.open].(types.TextContent)
	b.Text += delta
	em.out.Content[em.open] = b
	em.partial(types.EvTextDelta, em.open, delta, "", nil)
}

func (em *emitter) thinkingDelta(delta, signature string) {
	if em.open < 0 || em.openKd != "thinking" {
		em.closeOpen()
		em.out.Content = append(em.out.Content, types.ThinkingContent{Type: types.TypeThinking})
		em.open = len(em.out.Content) - 1
		em.openKd = "thinking"
		em.partial(types.EvThinkingStart, em.open, "", "", nil)
	}
	b := em.out.Content[em.open].(types.ThinkingContent)
	b.Thinking += delta
	if signature != "" {
		b.Signature = signature
	}
	em.out.Content[em.open] = b
	em.partial(types.EvThinkingDelta, em.open, delta, "", nil)
}

// appendToolCall adds a new tool call block and emits toolcall_start.
func (em *emitter) appendToolCall(tc types.ToolCall) int {
	em.closeOpen()
	em.out.Content = append(em.out.Content, tc)
	idx := len(em.out.Content) - 1
	t := tc
	em.partial(types.EvToolcallStart, idx, "", "", &t)
	return idx
}

// toolCallDelta updates the tool call block at idx with an argument fragment
// and emits toolcall_delta.
func (em *emitter) toolCallDelta(idx int, delta string, argsSoFar json.RawMessage) {
	tc := em.out.Content[idx].(types.ToolCall)
	tc.Arguments = argsSoFar
	em.out.Content[idx] = tc
	t := tc
	em.partial(types.EvToolcallDelta, idx, delta, "", &t)
}

// toolCallEnd finalizes the block at idx and emits toolcall_end.
func (em *emitter) toolCallEnd(idx int) {
	tc := em.out.Content[idx].(types.ToolCall)
	t := tc
	em.partial(types.EvToolcallEnd, idx, "", "", &t)
}

func (em *emitter) done(reason types.StopReason) {
	em.closeOpen()
	em.out.StopReason = reason
	em.ch <- types.DoneEvent{Kind: types.EvDone, Reason: reason, Message: em.out}
}

// fail terminates the stream with an error event. Aborted contexts yield
// reason "aborted", everything else "error".
func (em *emitter) fail(ctx context.Context, err error) {
	em.closeOpen()
	reason := types.StopError
	if streamAborted(ctx) {
		reason = types.StopAborted
	}
	em.out.StopReason = reason
	if err != nil {
		em.out.ErrorMessage = err.Error()
	}
	em.ch <- types.StreamErrorEvent{Kind: types.EvStreamError, Reason: reason, Message: em.out}
}
