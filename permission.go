package claudecli

// PermissionMode controls what the CLI is allowed to do.
type PermissionMode string

const (
	PermissionDefault     PermissionMode = "default"
	PermissionPlan        PermissionMode = "plan"
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	PermissionBypass      PermissionMode = "bypassPermissions"
	PermissionDontAsk     PermissionMode = "dontAsk"
	PermissionAuto        PermissionMode = "auto"
	// PermissionManual requires explicit approval for each tool use. As of CLI
	// 2.1.200 this is what the CLI's own "default" mode maps to.
	PermissionManual PermissionMode = "manual"
)
