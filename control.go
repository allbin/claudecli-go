package claudecli

import (
	"encoding/base64"
	"encoding/json"
)

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
	Subtype   string `json:"subtype"` // "success" or "error"
	RequestID string `json:"request_id"`
	Response  any    `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
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
	Content any    `json:"content"` // string or []ContentBlock
}

// ContentBlock is an opaque content block for multimodal messages.
// Create with TextBlock, ImageBlock, or DocumentBlock.
type ContentBlock struct {
	raw json.RawMessage
}

func (b ContentBlock) MarshalJSON() ([]byte, error) { return b.raw, nil }

// TextBlock creates a text content block.
func TextBlock(text string) ContentBlock {
	data, _ := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"text", text})
	return ContentBlock{raw: data}
}

// base64SourceBlock builds an image or document block with base64-encoded data.
func base64SourceBlock(blockType, mediaType string, data []byte) ContentBlock {
	raw, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}{
		Type: blockType,
		Source: struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		}{"base64", mediaType, base64.StdEncoding.EncodeToString(data)},
	})
	return ContentBlock{raw: raw}
}

// ImageBlock creates an image content block.
// mediaType: "image/png", "image/jpeg", "image/gif", or "image/webp".
func ImageBlock(mediaType string, data []byte) ContentBlock {
	return base64SourceBlock("image", mediaType, data)
}

// DocumentBlock creates a document content block (e.g. PDF).
func DocumentBlock(mediaType string, data []byte) ContentBlock {
	return base64SourceBlock("document", mediaType, data)
}
