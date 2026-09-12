package gptlive

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type FunctionCall struct{ CallID, Name, Arguments string }
type FunctionResult struct {
	CallID, Name, Output string
	IsError              bool
}
type BackendUsage struct {
	Model                                               string
	Input, Cached, CacheWrite, Output, Reasoning, Total int
}
type DelegationStarted struct{ ID string }

func (FunctionCall) isEvent()      {}
func (FunctionResult) isEvent()    {}
func (BackendUsage) isEvent()      {}
func (DelegationStarted) isEvent() {}

const toolTimeout = 30 * time.Second

// delegatedResponse follows one backend response: which calls it made and which
// have been answered. The continuation is sent only once both sets match.
type delegatedResponse struct {
	completed bool
	callIDs   map[string]bool
	returned  map[string]bool
}

func delegationKey(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func (s *Session) onDelegationCreated(ev ServerEvent) {
	if ev.Delegation == nil || ev.Delegation.ID == "" {
		logger.WarnCF("gptlive", "delegation without id", nil)
		return
	}
	s.emit(DelegationStarted{ID: ev.Delegation.ID})
}

// onResponseEvent runs on the read goroutine, tracking one backend response per
// delegation id: which function calls it made (response.output_item.done) and
// whether every one of them has been answered, so the response is continued
// (response.create) exactly once, after it completes and all calls return.
func (s *Session) onResponseEvent(ev ServerEvent) {
	if ev.Event == nil {
		return
	}
	key := delegationKey(ev.DelegationID)
	switch ev.Event.Type {
	case "response.created":
		s.delegationsMu.Lock()
		s.delegations[key] = &delegatedResponse{callIDs: map[string]bool{}, returned: map[string]bool{}}
		s.delegationsMu.Unlock()

	case "response.output_item.done":
		item := ev.Event.Item
		if item == nil || item.Type != "function_call" {
			return
		}
		if item.CallID == "" || item.Name == "" || item.Arguments == nil {
			logger.WarnCF("gptlive", "function call with missing fields", map[string]any{"call_id": item.CallID, "name": item.Name})
			return
		}
		s.delegationsMu.Lock()
		pending := s.delegations[key]
		if pending == nil {
			pending = &delegatedResponse{completed: true, callIDs: map[string]bool{}, returned: map[string]bool{}}
			s.delegations[key] = pending
		}
		pending.callIDs[item.CallID] = true
		s.callToDelegation[item.CallID] = key
		s.delegationsMu.Unlock()
		call := FunctionCall{CallID: item.CallID, Name: item.Name, Arguments: *item.Arguments}
		s.emit(call)
		go s.executeCall(key, call)

	case "response.completed":
		if r := ev.Event.Response; r != nil && r.Usage != nil {
			model := r.Model
			if model == "" {
				model = s.cfg.Backend.Model
			}
			u := r.Usage
			s.emit(BackendUsage{Model: model, Input: u.InputTokens, Cached: u.InputTokensDetails.CachedTokens,
				CacheWrite: u.InputTokensDetails.CacheWriteTokens, Output: u.OutputTokens,
				Reasoning: u.OutputTokensDetails.ReasoningTokens, Total: u.TotalTokens})
		}
		s.delegationsMu.Lock()
		if pending := s.delegations[key]; pending != nil {
			pending.completed = true
		}
		s.delegationsMu.Unlock()
		s.maybeContinue(key)

	case "response.failed", "response.incomplete":
		logger.WarnCF("gptlive", "backend response did not complete", map[string]any{"type": ev.Event.Type, "delegation_id": key})
		s.delegationsMu.Lock()
		if pending := s.delegations[key]; pending != nil {
			for id := range pending.callIDs {
				delete(s.callToDelegation, id)
			}
			delete(s.delegations, key)
		}
		s.delegationsMu.Unlock()
	}
}

// executeCall runs one backend function call on its own goroutine so a slow tool
// never blocks the read loop. It respects s.ctx for both the tool deadline and the
// eventual send, so it cannot outlive the session.
func (s *Session) executeCall(key string, call FunctionCall) {
	args := map[string]any{}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		args = map[string]any{}
	}
	output, isErr := "no tool executor configured", true
	if s.cfg.Tools != nil {
		ctx, cancel := context.WithTimeout(s.ctx, toolTimeout)
		output, isErr = s.cfg.Tools.Execute(ctx, call.Name, args)
		cancel()
	}
	if isErr {
		output = "error: " + output
	}
	s.emit(FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	s.send(responseItemCreateEvent{Type: EventResponseItemCreate, EventID: newEventID("tool_output_"),
		Item: FunctionCallOutput{Type: "function_call_output", CallID: call.CallID, Output: output}})
	s.delegationsMu.Lock()
	if pending := s.delegations[key]; pending != nil {
		pending.returned[call.CallID] = true
	}
	s.delegationsMu.Unlock()
	s.maybeContinue(key)
}

// maybeContinue sends response.create once the backend has finished asking and
// every call it made has an answer. A partial batch is never continued, and the
// pending state is cleared before the send so a duplicate completion or return
// cannot trigger it twice.
func (s *Session) maybeContinue(key string) {
	s.delegationsMu.Lock()
	pending := s.delegations[key]
	if pending == nil || !pending.completed || len(pending.callIDs) == 0 {
		s.delegationsMu.Unlock()
		return
	}
	for id := range pending.callIDs {
		if !pending.returned[id] {
			s.delegationsMu.Unlock()
			return
		}
	}
	delete(s.delegations, key)
	for id := range pending.callIDs {
		delete(s.callToDelegation, id)
	}
	s.delegationsMu.Unlock()
	s.send(simpleEvent{Type: EventResponseCreate, EventID: newEventID("response_create_")})
}

// resetDelegations clears delegation bookkeeping; callers use it when a reconnect
// starts a fresh session and any in-flight backend response is no longer valid.
func (s *Session) resetDelegations() {
	s.delegationsMu.Lock()
	defer s.delegationsMu.Unlock()
	s.delegations = map[string]*delegatedResponse{}
	s.callToDelegation = map[string]string{}
}
