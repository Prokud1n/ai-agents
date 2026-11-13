package anthropic

import (
	"encoding/json"
	"fmt"
)

type Role string

const (
	UserRole      Role = "user"
	AssistantRole Role = "assistant"
)

type ContentType string

const (
	TextContentType       ContentType = "text"
	ToolUseContentType    ContentType = "tool_use"
	ToolResultContentType ContentType = "tool_result"
)

type StopReason string

const (
	EndTurnStopReason   StopReason = "end_turn"
	ToolUseStopReason   StopReason = "tool_use"
	MaxTokensStopReason StopReason = "max_tokens"
)

// MessageRequest is the body of POST /v1/messages (the subset the agents need).
type MessageRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    []SystemBlock    `json:"system,omitempty"`
	Messages  []Message        `json:"messages"`
	Tools     []ToolDefinition `json:"tools,omitempty"`
}

// SystemBlock is a text block of the system prompt; CacheControl marks the end of a cacheable prefix.
type SystemBlock struct {
	Type         ContentType   `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type CacheControl struct {
	Type string `json:"type"`
}

// EphemeralCache is the default (5-minute) prompt cache.
func EphemeralCache() *CacheControl {
	return &CacheControl{Type: "ephemeral"}
}

type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a message content block: text, tool_use or tool_result.
// A block decoded from a response keeps its original JSON and is sent back unchanged,
// so block types and fields this client does not model survive the next turn.
type ContentBlock struct {
	Type ContentType `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	raw json.RawMessage
}

// contentBlockFields has the fields of ContentBlock without its JSON methods.
type contentBlockFields ContentBlock

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	var fields contentBlockFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("failed to decode content block: %w", err)
	}

	*b = ContentBlock(fields)
	b.raw = append(json.RawMessage(nil), data...)

	return nil
}

// MarshalJSON has a pointer receiver: encoding/json calls it for blocks inside slices,
// which is how blocks are always sent.
func (b *ContentBlock) MarshalJSON() ([]byte, error) {
	if len(b.raw) > 0 {
		return b.raw, nil
	}

	data, err := json.Marshal(contentBlockFields(*b))
	if err != nil {
		return nil, fmt.Errorf("failed to encode content block: %w", err)
	}

	return data, nil
}

func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: TextContentType, Text: text}
}

func ToolResultBlock(toolUseID, content string, isError bool) ContentBlock {
	return ContentBlock{Type: ToolResultContentType, ToolUseID: toolUseID, Content: content, IsError: isError}
}

// ToolDefinition describes a client tool; InputSchema is a JSON Schema object.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type MessageResponse struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason StopReason     `json:"stop_reason"`
	Usage      Usage          `json:"usage"`

	// RequestID comes from the request-id response header.
	RequestID string `json:"-"`
}

// Usage holds billed tokens. InputTokens excludes cached tokens: the full prompt is
// InputTokens + CacheCreationInputTokens + CacheReadInputTokens.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// Text joins the text blocks of the response.
func (r *MessageResponse) Text() string {
	return JoinText(r.Content)
}

// JoinText joins text blocks with a blank line.
func JoinText(blocks []ContentBlock) string {
	var text string

	for i := range blocks {
		if blocks[i].Type != TextContentType || blocks[i].Text == "" {
			continue
		}

		if text != "" {
			text += "\n\n"
		}

		text += blocks[i].Text
	}

	return text
}
