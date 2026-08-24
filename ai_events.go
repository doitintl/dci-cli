package main

// P2 of AI-SPEC (§8): the event protocol. The agent core (ai_session.go)
// emits these; renderers — the inline TUI, the one-shot runner, and any
// future client — consume them. The protocol carries data and declarative
// view information only, never ANSI or terminal formatting, so a different
// renderer can draw the same conversation. Versioned: a consumer checks
// aiEventSchemaVersion before trusting field shapes. Kept in a sibling file
// per the AGENTS.md chapter-split guidance.

import "time"

const aiEventSchemaVersion = 1

// aiEvent is the union carrier: exactly one pointer field is set per event.
// The JSON encoding is {"v":1,"turn_started":{...}} — the set field names the
// event kind, so consumers switch without a separate discriminator that could
// drift from the payload.
type aiEvent struct {
	V               int                `json:"v"`
	TurnStarted     *aiTurnStarted     `json:"turn_started,omitempty"`
	TextDelta       *aiTextDelta       `json:"text_delta,omitempty"`
	ThinkingDelta   *aiThinkingDelta   `json:"thinking_delta,omitempty"`
	ToolCallStarted *aiToolCallStarted `json:"tool_call_started,omitempty"`
	ToolResult      *aiToolResult      `json:"tool_result,omitempty"`
	ApprovalRequest *aiApprovalRequest `json:"approval_request,omitempty"`
	ContextSwitched *aiContextSwitched `json:"context_switched,omitempty"`
	LimitReached    *aiLimitReached    `json:"limit_reached,omitempty"`
	Error           *aiErrorEvent      `json:"error,omitempty"`
	TurnDone        *aiTurnDone        `json:"turn_done,omitempty"`
}

type aiTurnStarted struct {
	TurnID string `json:"turn_id"`
}

// aiTextDelta streams assistant narration as it generates. Deltas between a
// turn's start and its done event concatenate into the full markdown text.
type aiTextDelta struct {
	TurnID string `json:"turn_id"`
	Text   string `json:"text"`
}

// aiThinkingDelta streams the model's internal reasoning as it generates
// (models with adaptive thinking emit it before answering — often for tens of
// seconds on analytical questions). It never joins the transcript or the
// model-visible history shape; renderers use it to show live progress instead
// of a silent gap.
type aiThinkingDelta struct {
	TurnID string `json:"turn_id"`
	Text   string `json:"text"`
}

// aiToolCallStarted announces one tool invocation. By is "agent" for
// model-initiated calls and "user" for slash dispatches replayed into the
// transcript, so a renderer can badge provenance (AI-SPEC §8).
type aiToolCallStarted struct {
	TurnID   string   `json:"turn_id"`
	CallID   string   `json:"call_id"`
	Tool     string   `json:"tool"`
	Argv     []string `json:"argv,omitempty"`
	Customer string   `json:"customer,omitempty"`
	By       string   `json:"by"`
}

// aiToolResult reports one finished tool invocation. Data is the raw
// (contract-shaped) output the model sees; Error carries the structured
// error envelope verbatim when the call failed.
type aiToolResult struct {
	TurnID    string        `json:"turn_id"`
	CallID    string        `json:"call_id"`
	OK        bool          `json:"ok"`
	Data      string        `json:"data,omitempty"`
	Error     string        `json:"error,omitempty"`
	Customer  string        `json:"customer,omitempty"`
	Truncated bool          `json:"truncated"`
	Elapsed   time.Duration `json:"elapsed_ns"`
}

// aiApprovalRequest pauses the loop until the renderer answers (via
// aiUserInput with Kind aiInputApproval). Kind is "destructive" or
// "context_switch"; Summary is the CLI's own confirmation text.
type aiApprovalRequest struct {
	TurnID  string   `json:"turn_id"`
	CallID  string   `json:"call_id"`
	Kind    string   `json:"kind"`
	Summary string   `json:"summary"`
	Argv    []string `json:"argv,omitempty"`
}

type aiContextSwitched struct {
	From string `json:"from"`
	To   string `json:"to"`
	By   string `json:"by"` // "user" (/customer) or "agent" (set_customer_context)
}

type aiLimitReached struct {
	TurnID string `json:"turn_id"`
	Kind   string `json:"kind"` // "turns" or "tokens"
}

type aiErrorEvent struct {
	TurnID  string `json:"turn_id,omitempty"`
	Message string `json:"message"`
}

// aiTurnDone carries the turn's telemetry: token usage summed across API
// rounds, plus cost/latency counters for the eval harness (DCI_AI_STATS=1).
// Fields were added within schema v1 — additive changes keep old consumers
// working, so no version bump.
type aiTurnDone struct {
	TurnID       string        `json:"turn_id"`
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	CacheRead    int64         `json:"cache_read_input_tokens"`
	Rounds       int           `json:"rounds"`     // API rounds (streamer calls) in the turn
	ToolCalls    int           `json:"tool_calls"` // tool invocations actually started
	Wall         time.Duration `json:"wall_ns"`
	// FirstText is the time from turn start to the first answer text delta —
	// server-side thinking dominates it on analytical questions. Zero when the
	// turn produced no text (error, cancel, ceiling before any answer).
	FirstText time.Duration `json:"first_text_ns,omitempty"`
}

// --- Renderer → session -------------------------------------------------------

type aiInputKind int

const (
	aiInputChat aiInputKind = iota
	aiInputApproval
	aiInputCommandResult // a user slash dispatch joining the conversation (§4.4)
)

// aiUserInput is everything a renderer sends into a session: a question, an
// approval answer, or a finished slash command's result for context.
type aiUserInput struct {
	Kind     aiInputKind
	Text     string   // aiInputChat: the question
	CallID   string   // aiInputApproval: the request being answered
	Approved bool     // aiInputApproval
	Argv     []string // aiInputCommandResult: the dispatched command
	Output   string   // aiInputCommandResult: its output
	Customer string   // aiInputCommandResult: tenant it ran against
}
