package claudecli

import "fmt"

// Option configures a Run call. Options set at call time
// replace (not merge with) client-level defaults.
type Option func(*options)

type options struct {
	// client-only
	binaryPath string

	// model
	model         Model
	fallbackModel Model

	// prompts
	systemPrompt       string
	appendSystemPrompt string

	// tools
	tools           []string
	disallowedTools []string

	// permissions
	permissionMode PermissionMode

	// output
	jsonSchema string

	// budget
	maxBudget float64

	// session
	sessionID       string
	forkSession     bool
	continueSession bool

	// MCP
	mcpConfig       []string
	strictMCPConfig bool

	// execution
	workDir string
	effort  string
	env     map[string]string
}

// WithBinaryPath sets the Claude CLI binary path. Only effective when passed
// to New() (ignored at call time). Defaults to "claude".
func WithBinaryPath(path string) Option {
	return func(o *options) { o.binaryPath = path }
}

func WithModel(m Model) Option            { return func(o *options) { o.model = m } }
func WithFallbackModel(m Model) Option     { return func(o *options) { o.fallbackModel = m } }
func WithSystemPrompt(p string) Option     { return func(o *options) { o.systemPrompt = p } }
func WithAppendSystemPrompt(p string) Option {
	return func(o *options) { o.appendSystemPrompt = p }
}

func WithTools(tools ...string) Option            { return func(o *options) { o.tools = tools } }
func WithDisallowedTools(tools ...string) Option   { return func(o *options) { o.disallowedTools = tools } }
func WithPermissionMode(m PermissionMode) Option   { return func(o *options) { o.permissionMode = m } }
func WithJSONSchema(schema string) Option          { return func(o *options) { o.jsonSchema = schema } }
func WithMaxBudget(usd float64) Option             { return func(o *options) { o.maxBudget = usd } }
func WithSessionID(id string) Option               { return func(o *options) { o.sessionID = id } }
func WithForkSession() Option                      { return func(o *options) { o.forkSession = true } }
func WithContinue() Option                         { return func(o *options) { o.continueSession = true } }
func WithMCPConfig(configs ...string) Option       { return func(o *options) { o.mcpConfig = configs } }
func WithStrictMCPConfig() Option                  { return func(o *options) { o.strictMCPConfig = true } }
func WithWorkDir(dir string) Option                { return func(o *options) { o.workDir = dir } }
func WithEffort(level string) Option               { return func(o *options) { o.effort = level } }
func WithEnv(env map[string]string) Option         { return func(o *options) { o.env = env } }

func (o *options) buildArgs() []string {
	args := []string{"--print", "--verbose", "--output-format", "stream-json"}

	m := o.model
	if m == "" {
		m = DefaultModel
	}
	args = append(args, "--model", string(m))

	if o.fallbackModel != "" {
		args = append(args, "--fallback-model", string(o.fallbackModel))
	}

	if o.systemPrompt != "" {
		args = append(args, "--system-prompt", o.systemPrompt)
	}
	if o.appendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.appendSystemPrompt)
	}

	for _, t := range o.tools {
		args = append(args, "--allowedTools", t)
	}
	for _, t := range o.disallowedTools {
		args = append(args, "--disallowedTools", t)
	}

	if o.permissionMode != "" {
		args = append(args, "--permission-mode", string(o.permissionMode))
	}

	if o.jsonSchema != "" {
		args = append(args, "--json-schema", o.jsonSchema)
	}

	if o.maxBudget > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", o.maxBudget))
	}

	if o.sessionID != "" {
		args = append(args, "--session-id", o.sessionID)
		if o.forkSession {
			args = append(args, "--fork-session")
		}
	} else if o.continueSession {
		args = append(args, "--continue")
	} else {
		args = append(args, "--no-session-persistence")
	}

	for _, c := range o.mcpConfig {
		args = append(args, "--mcp-config", c)
	}
	if o.strictMCPConfig {
		args = append(args, "--strict-mcp-config")
	}

	if o.effort != "" {
		args = append(args, "--effort", o.effort)
	}

	return args
}

func resolveOptions(defaults []Option, overrides []Option) *options {
	opts := &options{}
	for _, o := range defaults {
		o(opts)
	}
	for _, o := range overrides {
		o(opts)
	}
	return opts
}
