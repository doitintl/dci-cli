package main

// P2 of AI-SPEC (§7): the conversation session — the headless agent core.
// A session owns the model loop: it takes user input (questions, approval
// answers, slash-command results), drives the Claude API with the tool
// surface from ai_tools.go and the prompt from ai_prompt.go, and emits
// protocol events (ai_events.go). Renderers — the inline TUI and the
// one-shot runner — only ever see the events channel, which is the seam a
// future remote client would replace (§7.2). The model transport is a
// function value so the loop is testable with scripted turns. Kept in a
// sibling file per the AGENTS.md chapter-split guidance.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// --- Settings (D1: user-supplied key; D2: model choice) ----------------------

const (
	aiSettingsFileName = "ai_settings.json"
	aiDefaultModel     = "claude-opus-5"
	aiMaxToolRounds    = 16
	// aiMaxOutputTokens bounds one API turn. Adaptive thinking counts against
	// max_tokens: analytical questions (aggregating a 200-row result) have
	// been measured thinking past 16k on their own, which killed the turn
	// with an output-token ceiling error before any answer text.
	aiMaxOutputTokens = 32000
	// aiDefaultEffort caps reasoning effort when nothing is configured.
	// Measured on the FinOps-12 suite (AI-FINOPS-SPEC §1), medium matched
	// unbounded reasoning answer-for-answer while cutting wall clock ~25%;
	// "default" opts back into the API default (no cap).
	aiDefaultEffort = "medium"
)

// aiKnownModels is the selectable set shown by /model. Other claude-* ids are
// accepted with a warning — the API is the final validator.
var aiKnownModels = []string{
	"claude-opus-5",
	"claude-sonnet-5",
	"claude-opus-4-8",
	"claude-haiku-4-5",
}

type aiSettings struct {
	APIKey string `json:"api_key,omitempty"`
	Model  string `json:"model,omitempty"`
	// Effort caps the model's reasoning depth ("low", "medium", "high";
	// "default" = uncapped API default; empty = aiDefaultEffort). Analytical
	// questions can reason server-side for a minute-plus before the first
	// answer token — lower effort trades some of that depth for latency.
	Effort string `json:"effort,omitempty"`
}

func aiSettingsPath(configDir string) string {
	return filepath.Join(configDir, aiSettingsFileName)
}

func loadAISettings(configDir string) aiSettings {
	var settings aiSettings
	data, err := os.ReadFile(aiSettingsPath(configDir))
	if err != nil {
		return settings
	}
	_ = json.Unmarshal(data, &settings)
	return settings
}

func saveAISettings(configDir string, settings aiSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(aiSettingsPath(configDir), append(data, '\n'), 0o600)
}

// resolveAIKey returns the API key: the environment wins over the settings
// file, matching how the CLI treats DCI_API_BASE_URL.
func resolveAIKey(settings aiSettings) string {
	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(settings.APIKey)
}

func resolveAIModel(settings aiSettings) string {
	if model := strings.TrimSpace(os.Getenv("DCI_AI_MODEL")); model != "" {
		return model
	}
	if model := strings.TrimSpace(settings.Model); model != "" {
		return model
	}
	return aiDefaultModel
}

// resolveAIEffort returns the reasoning-effort cap: DCI_AI_EFFORT wins over
// the settings file (same precedence as the API key). "low"/"medium"/"high"
// cap the effort; "default" removes the cap (the API's unbounded adaptive
// thinking — note "high" is still a cap, not the API default). An invalid
// value falls through to the next source rather than failing a session over
// a config typo; with nothing valid configured the cap is aiDefaultEffort.
func resolveAIEffort(settings aiSettings) string {
	for _, effort := range []string{os.Getenv("DCI_AI_EFFORT"), settings.Effort} {
		switch strings.ToLower(strings.TrimSpace(effort)) {
		case "low":
			return "low"
		case "medium":
			return "medium"
		case "high":
			return "high"
		case "default":
			return "" // no cap: the API default
		}
	}
	return aiDefaultEffort
}

// aiValidateAPIKey applies shape checks before persisting a key: a wrong
// paste silently breaks every question with a 401, so obvious mistakes are
// rejected at save time (Anthropic keys start with sk-).
func aiValidateAPIKey(key string) error {
	if key == "" {
		return fmt.Errorf("the key is empty")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("the key contains whitespace — check the paste")
	}
	if !strings.HasPrefix(key, "sk-") || len(key) < 20 {
		return fmt.Errorf("that does not look like an Anthropic API key (they start with sk-)")
	}
	return nil
}

func aiValidateModel(model string) error {
	if !strings.HasPrefix(model, "claude-") {
		return fmt.Errorf("model %q does not look like a Claude model id (expected claude-…)", model)
	}
	return nil
}

func aiModelIsKnown(model string) bool {
	for _, known := range aiKnownModels {
		if known == model {
			return true
		}
	}
	return false
}

// --- Session ------------------------------------------------------------------

// conversationSession is the renderer-facing seam (AI-SPEC §7.2). The local
// implementation below is the only planned one (D1); a remote client would
// implement the same interface over a stream.
type conversationSession interface {
	Send(input aiUserInput) error
	Events() <-chan aiEvent
	Cancel()
	Close() error
}

// aiModelStreamer is one streamed model turn: emit text deltas via onDelta
// and thinking deltas via onThinking (models with adaptive thinking reason
// before answering; dropping those deltas leaves renderers a silent gap),
// return the accumulated message. A function value so tests script turns.
type aiModelStreamer func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error)

type localAISession struct {
	configDir    string
	model        string
	effort       string // reasoning-effort cap; "" = API default
	tenantAware  bool
	stablePrompt string
	streamer     aiModelStreamer
	executor     *aiToolExecutor

	events    chan aiEvent
	approvals chan aiUserInput
	done      chan struct{} // closed by Close; emit drains against it

	mu      sync.Mutex
	history []anthropic.MessageParam
	pending []string // injected slash results, consumed by the next question
	running bool
	cancel  context.CancelFunc
	turnSeq int
	closed  bool
}

// errAITurnRunning is returned by Send(chat) while a turn is in flight.
var errAITurnRunning = errors.New("a turn is already running — wait for it or press esc to cancel")

// newLocalAISession builds the local session. The key must already be
// resolved and non-empty; renderers gate on aiSessionAvailable first.
func newLocalAISession(configDir, apiKey, model string, catalog []aiCatalogEntry) *localAISession {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	isDoer := cachedTokenIsDoer()
	tenantAware := isDoer || readCustomerContext(configDir) != ""
	return &localAISession{
		configDir:    configDir,
		model:        model,
		effort:       resolveAIEffort(loadAISettings(configDir)),
		tenantAware:  tenantAware,
		stablePrompt: aiSystemPrompt(catalog, tenantAware, isDoer),
		streamer:     newAnthropicStreamer(client),
		executor:     newAIToolExecutor(configDir),
		events:       make(chan aiEvent, 64),
		approvals:    make(chan aiUserInput, 1),
		done:         make(chan struct{}),
	}
}

func newAnthropicStreamer(client anthropic.Client) aiModelStreamer {
	debugStream := os.Getenv("DCI_AI_DEBUG_STREAM") == "1"
	return func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error) {
		stream := client.Messages.NewStreaming(ctx, params)
		var message anthropic.Message
		started := time.Now()
		for stream.Next() {
			event := stream.Current()
			if debugStream {
				kind := event.Type
				if deltaEvent, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
					kind += "/" + deltaEvent.Delta.Type
				}
				fmt.Fprintf(os.Stderr, "[ai-stream %7.2fs] %s\n", time.Since(started).Seconds(), kind)
			}
			if err := message.Accumulate(event); err != nil {
				return message, err
			}
			if deltaEvent, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
				switch delta := deltaEvent.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					if delta.Text != "" {
						onDelta(delta.Text)
					}
				case anthropic.ThinkingDelta:
					if delta.Thinking != "" {
						onThinking(delta.Thinking)
					}
				}
			}
		}
		return message, stream.Err()
	}
}

func (s *localAISession) Events() <-chan aiEvent { return s.events }

// ClearCustomerOverride drops the agent's session-scoped context switch —
// the TUI calls it (via interface assertion) when the user persists a context
// with /customer, which must win over any earlier agent switch.
func (s *localAISession) ClearCustomerOverride() { s.executor.ClearCustomerOverride() }

// SetModel applies a model change (D2) to future requests. History is kept:
// thinking blocks from another model are dropped server-side, so a mid-
// conversation switch is safe.
func (s *localAISession) SetModel(model string) {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()
}

func (s *localAISession) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close cancels any in-flight turn and stops future emits. The events
// channel is deliberately never closed: a turn goroutine may still be
// draining, and a send racing a close would panic. Emitters select against
// the done channel instead, so they always unblock.
func (s *localAISession) Close() error {
	s.Cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

func (s *localAISession) Send(input aiUserInput) error {
	switch input.Kind {
	case aiInputApproval:
		select {
		case s.approvals <- input:
		default:
		}
		return nil

	case aiInputCommandResult:
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pending = append(s.pending, aiCommandResultContext(input))
		// Keep only the most recent injections: old screen content ages out
		// the same way it scrolls away for the human.
		if len(s.pending) > 5 {
			s.pending = s.pending[len(s.pending)-5:]
		}
		return nil

	case aiInputChat:
		s.mu.Lock()
		if s.running {
			s.mu.Unlock()
			return errAITurnRunning
		}
		s.running = true
		s.turnSeq++
		turnID := fmt.Sprintf("t%d", s.turnSeq)
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		userMessage := s.composeUserMessageLocked(input.Text)
		s.history = append(s.history, userMessage)
		s.mu.Unlock()

		go s.runTurn(ctx, cancel, turnID)
		return nil
	}
	return fmt.Errorf("unknown input kind %d", input.Kind)
}

// aiCommandResultContext shapes one user-run slash command for model context
// (§4.4): capped like a tool result, tagged with the tenant it ran against.
func aiCommandResultContext(input aiUserInput) string {
	output, truncated := aiCapToolResult(input.Output)
	var b strings.Builder
	b.WriteString("The user ran `dci " + strings.Join(input.Argv, " ") + "`")
	if input.Customer != "" {
		b.WriteString(" against customer context " + input.Customer)
	}
	b.WriteString(". Output:\n")
	b.WriteString(output)
	if truncated {
		b.WriteString("\n[truncated]")
	}
	return b.String()
}

func (s *localAISession) composeUserMessageLocked(question string) anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(s.pending)+1)
	for _, injected := range s.pending {
		blocks = append(blocks, anthropic.NewTextBlock(injected))
	}
	s.pending = nil
	blocks = append(blocks, anthropic.NewTextBlock(question))
	return anthropic.NewUserMessage(blocks...)
}

func (s *localAISession) emit(event aiEvent) {
	event.V = aiEventSchemaVersion
	select {
	case <-s.done:
	case s.events <- event:
	}
}

func (s *localAISession) tools() []anthropic.ToolUnionParam {
	runTool := anthropic.ToolParam{
		Name:        aiToolRunCommand,
		Description: anthropic.String("Run one dci CLI command and return its output. argv is the words after \"dci\", e.g. [\"list-budgets\", \"--output\", \"json\"]. Run a command with --help first to learn its flags."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"argv": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Command line as separate words, without the leading \"dci\"",
				},
			},
			Required: []string{"argv"},
		},
	}
	tools := []anthropic.ToolUnionParam{{OfTool: &runTool}}
	if s.tenantAware {
		switchTool := anthropic.ToolParam{
			Name:        aiToolSetCustomer,
			Description: anthropic.String("Switch the session's active customer context (tenant). Use only when the user asks about a different customer. The switch is shown to the user."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"customer": map[string]any{"type": "string", "description": "Customer domain (acme.com), ID, or URL display name"},
				},
				Required: []string{"customer"},
			},
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &switchTool})
	}
	return tools
}

func (s *localAISession) params() anthropic.MessageNewParams {
	s.mu.Lock()
	history := append([]anthropic.MessageParam{}, s.history...)
	model := s.model
	s.mu.Unlock()
	stable := anthropic.TextBlockParam{Text: s.stablePrompt, CacheControl: anthropic.NewCacheControlEphemeralParam()}
	volatile := anthropic.TextBlockParam{Text: aiVolatileSystem(s.configDir, time.Now(), s.executor.CustomerOverride())}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: aiMaxOutputTokens,
		System:    []anthropic.TextBlockParam{stable, volatile},
		Messages:  history,
		Tools:     s.tools(),
	}
	if s.effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(s.effort)}
	}
	return params
}

func (s *localAISession) runTurn(ctx context.Context, cancel context.CancelFunc, turnID string) {
	defer func() {
		cancel()
		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
	}()

	s.emit(aiEvent{TurnStarted: &aiTurnStarted{TurnID: turnID}})
	started := time.Now()
	usage := aiTurnDone{TurnID: turnID}
	finish := func() {
		usage.Wall = time.Since(started)
		s.emit(aiEvent{TurnDone: &usage})
	}

	for round := 0; round < aiMaxToolRounds; round++ {
		usage.Rounds++
		message, err := s.streamer(ctx, s.params(), func(text string) {
			if usage.FirstText == 0 {
				usage.FirstText = time.Since(started)
			}
			s.emit(aiEvent{TextDelta: &aiTextDelta{TurnID: turnID, Text: text}})
		}, func(text string) {
			s.emit(aiEvent{ThinkingDelta: &aiThinkingDelta{TurnID: turnID, Text: text}})
		})
		if err != nil {
			if ctx.Err() != nil {
				s.emit(aiEvent{Error: &aiErrorEvent{TurnID: turnID, Message: "turn canceled"}})
			} else {
				s.emit(aiEvent{Error: &aiErrorEvent{TurnID: turnID, Message: err.Error()}})
			}
			finish()
			return
		}
		usage.InputTokens += message.Usage.InputTokens
		usage.OutputTokens += message.Usage.OutputTokens
		usage.CacheRead += message.Usage.CacheReadInputTokens

		if param, keep := aiHistoryParam(message); keep {
			s.mu.Lock()
			s.history = append(s.history, param)
			s.mu.Unlock()
		}

		if message.StopReason == anthropic.StopReasonMaxTokens {
			// Cut mid-generation: aiHistoryParam stripped any half-generated
			// tool calls, so the next question starts from a valid history.
			s.emit(aiEvent{LimitReached: &aiLimitReached{TurnID: turnID, Kind: "output-token"}})
			finish()
			return
		}
		if message.StopReason != anthropic.StopReasonToolUse {
			finish()
			return
		}

		// Every tool_use in the assistant message must gain a tool_result in
		// the next user message — including on cancel, or the dangling
		// tool_use makes the API reject every later question with a 400.
		toolResults, executed, canceled := s.executeToolCalls(ctx, turnID, message.Content)
		usage.ToolCalls += executed
		if len(toolResults) > 0 {
			s.mu.Lock()
			s.history = append(s.history, anthropic.NewUserMessage(toolResults...))
			s.mu.Unlock()
		}
		if canceled {
			s.emit(aiEvent{Error: &aiErrorEvent{TurnID: turnID, Message: "turn canceled"}})
			finish()
			return
		}
		if len(toolResults) == 0 {
			finish()
			return
		}
	}

	s.emit(aiEvent{LimitReached: &aiLimitReached{TurnID: turnID, Kind: "turns"}})
	finish()
}

// aiToolConcurrency bounds how many run_dci_command children run at once.
// The model batches independent lookups in one assistant message (it is
// prompted to); running them serially made the batch's wall clock the sum of
// its slowest members.
const aiToolConcurrency = 4

// executeToolCalls answers every tool_use block in one assistant message.
// Consecutive run_dci_command calls execute concurrently (bounded by
// aiToolConcurrency); set_customer_context is a barrier — it mutates the
// override every later child inherits as environment, so it applies in
// message order and never alongside a running command. canceled=true means
// the user canceled mid-message; every remaining tool_use still gains a
// canceled result, or the dangling tool_use makes the API reject every later
// question with a 400. executed counts the calls that actually started, for
// the turn's telemetry.
func (s *localAISession) executeToolCalls(ctx context.Context, turnID string, blocks []anthropic.ContentBlockUnion) (results []anthropic.ContentBlockParamUnion, executed int, canceled bool) {
	toolUses := make([]anthropic.ToolUseBlock, 0, len(blocks))
	for _, block := range blocks {
		if toolUse, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			toolUses = append(toolUses, toolUse)
		}
	}
	results = make([]anthropic.ContentBlockParamUnion, len(toolUses))
	for start := 0; start < len(toolUses); {
		if !canceled && ctx.Err() != nil {
			canceled = true
		}
		if canceled {
			results[start] = aiCanceledToolResult(toolUses[start].ID)
			start++
			continue
		}
		if toolUses[start].Name != aiToolRunCommand {
			results[start] = s.executeSerialToolCall(turnID, toolUses[start])
			executed++
			start++
			continue
		}
		end := start + 1
		for end < len(toolUses) && toolUses[end].Name == aiToolRunCommand {
			end++
		}
		batchExecuted, batchCanceled := s.executeCommandBatch(ctx, turnID, toolUses[start:end], results[start:end])
		executed += batchExecuted
		canceled = batchCanceled
		start = end
	}
	return results, executed, canceled
}

// aiFirstPass is one run_dci_command call's state after its concurrent first
// execution: either settled with a final result, waiting on the destructive
// approval round-trip, or aborted by cancel.
type aiFirstPass struct {
	result        anthropic.ContentBlockParamUnion
	settled       bool
	needsApproval bool
	aborted       bool
	input         aiRunCommandInput
	summary       string
	started       time.Time
}

// executeCommandBatch runs one message's consecutive run_dci_command calls
// concurrently up to their first outcome, then resolves any destructive
// approvals one at a time — the renderers can only present one approval
// question at a time (awaitApproval). Results land in the callers' slice at
// the calls' original positions; on cancel every unanswered call is
// backfilled with a canceled result. executed counts the calls that actually
// started (a worker still queued when the user canceled never ran).
func (s *localAISession) executeCommandBatch(ctx context.Context, turnID string, calls []anthropic.ToolUseBlock, results []anthropic.ContentBlockParamUnion) (executed int, canceled bool) {
	passes := make([]aiFirstPass, len(calls))
	semaphore := make(chan struct{}, aiToolConcurrency)
	var started atomic.Int64
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			if ctx.Err() != nil {
				passes[i] = aiFirstPass{aborted: true}
				return
			}
			started.Add(1)
			passes[i] = s.runCommandFirstPass(ctx, turnID, calls[i])
		}(i)
	}
	wg.Wait()
	executed = int(started.Load())

	answered := make([]bool, len(calls))
	pending := make([]int, 0, len(calls))
	for i, pass := range passes {
		switch {
		case pass.aborted:
			canceled = true
		case pass.settled:
			results[i] = pass.result
			answered[i] = true
		case pass.needsApproval:
			pending = append(pending, i)
		}
	}
	for _, i := range pending {
		if canceled {
			break
		}
		result, aborted := s.resolveApproval(ctx, turnID, calls[i], passes[i])
		if aborted {
			canceled = true
			break
		}
		results[i] = result
		answered[i] = true
	}
	if canceled {
		for i := range results {
			if !answered[i] {
				results[i] = aiCanceledToolResult(calls[i].ID)
			}
		}
	}
	return executed, canceled
}

// runCommandFirstPass executes one run_dci_command call up to its first
// outcome. Safe to run concurrently with its batch siblings: the executor's
// override is read-locked, and the approval round-trip (which must serialize)
// is deferred to resolveApproval.
func (s *localAISession) runCommandFirstPass(ctx context.Context, turnID string, toolUse anthropic.ToolUseBlock) aiFirstPass {
	var input aiRunCommandInput
	if err := json.Unmarshal([]byte(toolUse.JSON.Input.Raw()), &input); err != nil {
		return aiFirstPass{settled: true, result: anthropic.NewToolResultBlock(toolUse.ID, aiToolError("BAD_TOOL_INPUT", err.Error(), ""), true)}
	}
	customer := s.executor.EffectiveCustomer()
	s.emit(aiEvent{ToolCallStarted: &aiToolCallStarted{
		TurnID: turnID, CallID: toolUse.ID, Tool: aiToolRunCommand, Argv: input.Argv, Customer: customer, By: "agent",
	}})
	started := time.Now()
	outcome := s.executor.RunCommand(ctx, input, false)
	if outcome.NeedsApproval {
		return aiFirstPass{needsApproval: true, input: input, summary: outcome.Summary, started: started}
	}
	if ctx.Err() != nil {
		return aiFirstPass{aborted: true}
	}
	s.emit(aiEvent{ToolResult: &aiToolResult{
		TurnID: turnID, CallID: toolUse.ID, OK: !outcome.IsError,
		Data: outcome.Data, Customer: customer, Truncated: outcome.Truncated, Elapsed: time.Since(started),
	}})
	return aiFirstPass{settled: true, result: anthropic.NewToolResultBlock(toolUse.ID, outcome.Data, outcome.IsError)}
}

// resolveApproval finishes one destructive call the concurrent first pass
// parked: ask the human, then re-run with --yes or shape the decline.
// aborted=true means the context was canceled while waiting or running.
func (s *localAISession) resolveApproval(ctx context.Context, turnID string, toolUse anthropic.ToolUseBlock, pass aiFirstPass) (anthropic.ContentBlockParamUnion, bool) {
	s.emit(aiEvent{ApprovalRequest: &aiApprovalRequest{
		TurnID: turnID, CallID: toolUse.ID, Kind: "destructive", Summary: pass.summary, Argv: pass.input.Argv,
	}})
	approved, aborted := s.awaitApproval(ctx, toolUse.ID)
	if aborted {
		return anthropic.ContentBlockParamUnion{}, true
	}
	var outcome aiToolOutcome
	if approved {
		outcome = s.executor.RunCommand(ctx, pass.input, true)
	} else {
		outcome = aiToolOutcome{
			Data:    aiToolError("DESTRUCTIVE_DECLINED", "the user declined this destructive command — do not retry it", ""),
			IsError: true,
		}
	}
	if ctx.Err() != nil {
		return anthropic.ContentBlockParamUnion{}, true
	}
	s.emit(aiEvent{ToolResult: &aiToolResult{
		TurnID: turnID, CallID: toolUse.ID, OK: !outcome.IsError,
		Data: outcome.Data, Customer: s.executor.EffectiveCustomer(), Truncated: outcome.Truncated, Elapsed: time.Since(pass.started),
	}})
	return anthropic.NewToolResultBlock(toolUse.ID, outcome.Data, outcome.IsError), false
}

// executeSerialToolCall runs one non-batchable tool_use block —
// set_customer_context (a barrier: see executeToolCalls) or an unknown tool.
func (s *localAISession) executeSerialToolCall(turnID string, toolUse anthropic.ToolUseBlock) anthropic.ContentBlockParamUnion {
	switch toolUse.Name {
	case aiToolSetCustomer:
		var input aiSetCustomerInput
		if err := json.Unmarshal([]byte(toolUse.JSON.Input.Raw()), &input); err != nil {
			return anthropic.NewToolResultBlock(toolUse.ID, aiToolError("BAD_TOOL_INPUT", err.Error(), ""), true)
		}
		s.emit(aiEvent{ToolCallStarted: &aiToolCallStarted{
			TurnID: turnID, CallID: toolUse.ID, Tool: aiToolSetCustomer, Customer: s.executor.EffectiveCustomer(), By: "agent",
		}})
		started := time.Now()
		from, to, outcome := s.executor.SetCustomer(input)
		if !outcome.IsError {
			s.emit(aiEvent{ContextSwitched: &aiContextSwitched{From: from, To: to, By: "agent"}})
		}
		s.emit(aiEvent{ToolResult: &aiToolResult{
			TurnID: turnID, CallID: toolUse.ID, OK: !outcome.IsError,
			Data: outcome.Data, Customer: to, Elapsed: time.Since(started),
		}})
		return anthropic.NewToolResultBlock(toolUse.ID, outcome.Data, outcome.IsError)

	default:
		return anthropic.NewToolResultBlock(toolUse.ID, aiToolError("UNKNOWN_TOOL", toolUse.Name, ""), true)
	}
}

// aiHistoryParam shapes one assistant message for the conversation history.
// A message cut by the output-token limit may end in a half-generated
// tool_use block whose input is partial JSON; replaying that makes the API
// reject every later request. Those blocks are stripped, and a message left
// with no text at all is dropped entirely.
func aiHistoryParam(message anthropic.Message) (anthropic.MessageParam, bool) {
	param := message.ToParam()
	if message.StopReason != anthropic.StopReasonMaxTokens {
		return param, true
	}
	kept := make([]anthropic.ContentBlockParamUnion, 0, len(param.Content))
	hasText := false
	for _, block := range param.Content {
		if block.OfToolUse != nil {
			continue
		}
		if block.OfText != nil {
			hasText = true
		}
		kept = append(kept, block)
	}
	param.Content = kept
	return param, hasText
}

// aiCanceledToolResult satisfies one tool_use the user canceled out of, so
// the history stays API-valid and the model knows the command never ran.
func aiCanceledToolResult(callID string) anthropic.ContentBlockParamUnion {
	return anthropic.NewToolResultBlock(callID,
		aiToolError("CANCELED", "the user canceled the turn before this command finished — do not assume it ran", ""), true)
}

// awaitApproval blocks the loop on the human's answer, draining stale answers
// for other call ids.
func (s *localAISession) awaitApproval(ctx context.Context, callID string) (approved, aborted bool) {
	for {
		select {
		case <-ctx.Done():
			return false, true
		case answer := <-s.approvals:
			if answer.CallID == callID || answer.CallID == "" {
				return answer.Approved, false
			}
		}
	}
}
