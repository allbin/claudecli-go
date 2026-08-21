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
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`

	// PermissionSuggestions are ready-made permission updates the CLI
	// derived for this prompt — the "always allow" flow. Echo the ones the
	// user accepted back via PermissionResponse.UpdatedPermissions rather
	// than deriving rules from the tool input: a suggestion can encode
	// compound-bash logic or a directory grant that is easy to get wrong.
	// Each entry is an opaque PermissionUpdate object.
	PermissionSuggestions []json.RawMessage `json:"permission_suggestions,omitempty"`

	// ToolUseID identifies the tool_use block being gated. Needed to
	// attribute a prompt to the call that raised it.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// AgentID is set when the call originated inside a subagent, and is how
	// a host routes the prompt to the right subagent in its UI.
	AgentID string `json:"agent_id,omitempty"`

	// DecisionReason explains why the prompt escalated, for the consent line
	// of a dialog. May contain ANSI escapes — sanitize before rendering.
	DecisionReason string `json:"decision_reason,omitempty"`
	// DecisionReasonType is the structured discriminator behind
	// DecisionReason: "rule", "mode", "subcommandResults",
	// "permissionPromptTool", "hook", "asyncAgent", "sandboxOverride",
	// "workingDir", "safetyCheck", "classifier", or "other". Make policy on
	// this rather than parsing DecisionReason.
	DecisionReasonType string `json:"decision_reason_type,omitempty"`
	// ClassifierApprovable is set only when a safety check is involved.
	// False means at least one check needs manual approval.
	ClassifierApprovable *bool `json:"classifier_approvable,omitempty"`

	// Title, DisplayName and Description are presentation fields for a
	// permission dialog.
	Title       string `json:"title,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`

	// BlockedPath is the filesystem path that triggered a path-based denial.
	BlockedPath string `json:"blocked_path,omitempty"`
	// SuppressAlwaysAllowRule is true when a dialog must not offer a
	// persistent "don't ask again" option: accepting would write a rule
	// broader than this prompt.
	SuppressAlwaysAllowRule bool `json:"suppress_always_allow_rule,omitempty"`
	// RequiresUserInteraction is true when one-tap approve/deny must not be
	// offered because the tool's own card is the interaction surface.
	RequiresUserInteraction bool `json:"requires_user_interaction,omitempty"`
}

// PermissionResponse is returned by the ToolPermissionFunc callback.
type PermissionResponse struct {
	Allow        bool
	UpdatedInput json.RawMessage
	DenyMessage  string

	// UpdatedPermissions are permission updates to apply along with an
	// allow — the "always allow" flow. Pass through the entries from
	// ToolPermissionRequest.PermissionSuggestions the user accepted.
	// Ignored when Allow is false.
	UpdatedPermissions []json.RawMessage

	// Interrupt stops the turn outright instead of returning the denial to
	// the model. Set it when the user declined with no further guidance;
	// leave it false when DenyMessage tells the model what to do instead.
	// Ignored when Allow is true.
	Interrupt bool
}

// ToolPermissionFunc is called when the CLI requests permission to use a tool.
//
// It receives only the tool name and input. To see the rest of the request —
// the tool_use id, the originating subagent, the escalation reason, and the
// CLI's permission suggestions — use WithCanUseToolRequest instead.
type ToolPermissionFunc func(toolName string, input json.RawMessage) (*PermissionResponse, error)

// ToolPermissionRequestFunc is called when the CLI requests permission to use
// a tool, and receives the full request rather than just name and input.
type ToolPermissionRequestFunc func(req ToolPermissionRequest) (*PermissionResponse, error)

// Question represents a single question from an AskUserQuestion tool call.
type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header,omitempty"`
	Options     []QuestionOption `json:"options,omitempty"`
	MultiSelect bool             `json:"multiSelect,omitempty"`
}

// QuestionOption is a selectable option within a Question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// UserInputFunc receives parsed questions and returns a map of question text -> selected answer(s).
// For multiSelect questions, multiple answers can be joined with newlines or returned as a JSON array.
type UserInputFunc func(questions []Question) (answers map[string]string, err error)

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
