package claudecli

import "encoding/json"

// Control message types for the bidirectional protocol.

// rawControlRequestBody has the common subtype field.
type rawControlRequestBody struct {
	Subtype string `json:"subtype"`
}

// rawControlResponse is sent back to CLI via stdin.
type rawControlResponse struct {
	Type     string              `json:"type"` // "control_response"
	Response controlResponseBody `json:"response"`
}

type controlResponseBody struct {
	Subtype   string      `json:"subtype"`              // "success" or "error"
	RequestID string      `json:"request_id"`
	Response  any `json:"response,omitempty"`
	Error     string      `json:"error,omitempty"`
}

// ToolPermissionRequest is the data inside a "can_use_tool" control request.
type ToolPermissionRequest struct {
	ToolName              string            `json:"tool_name"`
	Input                 json.RawMessage   `json:"input"`
	PermissionSuggestions []json.RawMessage `json:"permission_suggestions,omitempty"`
}

// PermissionResponse is returned by the ToolPermissionFunc callback.
type PermissionResponse struct {
	Allow        bool
	UpdatedInput json.RawMessage
	DenyMessage  string
}

// ToolPermissionFunc is called when the CLI requests permission to use a tool.
type ToolPermissionFunc func(toolName string, input json.RawMessage) (*PermissionResponse, error)

// controlResult is used internally for tracking pending control request responses.
type controlResult struct {
	Response json.RawMessage
	Err      error
}

// userMessage is the JSON structure sent to CLI for user prompts.
type userMessage struct {
	Type            string      `json:"type"`
	SessionID       string      `json:"session_id,omitempty"`
	Message         messageBody `json:"message"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
	UUID            string      `json:"uuid,omitempty"`
}

type messageBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
