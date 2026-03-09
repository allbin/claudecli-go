package claudecli

// PermissionMode controls what the CLI is allowed to do.
type PermissionMode string

const (
	PermissionDefault      PermissionMode = "default"
	PermissionPlan         PermissionMode = "plan"
	PermissionAcceptEdits  PermissionMode = "acceptEdits"
	PermissionBypass       PermissionMode = "bypassPermissions"
	PermissionDontAsk      PermissionMode = "dontAsk"
	PermissionAuto         PermissionMode = "auto"
)
