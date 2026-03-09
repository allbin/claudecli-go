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
	systemPrompt           string
	systemPromptFile       string
	appendSystemPrompt     string
	appendSystemPromptFile string

	// tools
	tools           []string
	disallowedTools []string
	builtinTools    []string

	// permissions
	permissionMode PermissionMode

	// output
	jsonSchema string

	// budget and limits
	maxBudget float64
	maxTurns  int

	// session
	sessionID       string
	forkSession     bool
	continueSession bool

	// MCP
	mcpConfig       []string
	strictMCPConfig bool

	// agents
	agent    string
	agentDef string

	// execution
	addDirs                []string
	workDir                string
	effort                 string
	env                    map[string]string
	includePartialMessages bool
}

// WithBinaryPath sets the Claude CLI binary path. Only effective when passed
// to New() (ignored at call time). Defaults to "claude".
func WithBinaryPath(path string) Option {
	return func(o *options) { o.binaryPath = path }
}

func WithModel(m Model) Option        { return func(o *options) { o.model = m } }
func WithFallbackModel(m Model) Option { return func(o *options) { o.fallbackModel = m } }

func WithSystemPrompt(p string) Option     { return func(o *options) { o.systemPrompt = p } }
func WithSystemPromptFile(p string) Option { return func(o *options) { o.systemPromptFile = p } }
func WithAppendSystemPrompt(p string) Option {
	return func(o *options) { o.appendSystemPrompt = p }
}
func WithAppendSystemPromptFile(p string) Option {
	return func(o *options) { o.appendSystemPromptFile = p }
}

func WithTools(tools ...string) Option          { return func(o *options) { o.tools = tools } }
func WithDisallowedTools(tools ...string) Option { return func(o *options) { o.disallowedTools = tools } }

// WithBuiltinTools restricts which built-in tools are available.
// Use "default" for all tools, "" to disable all, or specific names like "Bash", "Edit", "Read".
// Different from WithTools which controls permission prompts — this controls tool availability.
func WithBuiltinTools(tools ...string) Option { return func(o *options) { o.builtinTools = tools } }

func WithPermissionMode(m PermissionMode) Option { return func(o *options) { o.permissionMode = m } }
func WithJSONSchema(schema string) Option        { return func(o *options) { o.jsonSchema = schema } }
func WithMaxBudget(usd float64) Option           { return func(o *options) { o.maxBudget = usd } }
func WithMaxTurns(n int) Option                  { return func(o *options) { o.maxTurns = n } }
func WithSessionID(id string) Option             { return func(o *options) { o.sessionID = id } }
func WithForkSession() Option                    { return func(o *options) { o.forkSession = true } }
func WithContinue() Option                       { return func(o *options) { o.continueSession = true } }
func WithMCPConfig(configs ...string) Option     { return func(o *options) { o.mcpConfig = configs } }
func WithStrictMCPConfig() Option                { return func(o *options) { o.strictMCPConfig = true } }

// WithAgent selects a named agent for the session.
func WithAgent(name string) Option { return func(o *options) { o.agent = name } }

// WithAgentDef defines custom agents via a JSON string.
// Example: `{"reviewer": {"description": "Reviews code", "prompt": "You are a code reviewer"}}`.
func WithAgentDef(jsonDef string) Option { return func(o *options) { o.agentDef = jsonDef } }

// WithAddDirs adds directories the CLI tools can access beyond the working directory.
func WithAddDirs(dirs ...string) Option { return func(o *options) { o.addDirs = dirs } }

func WithWorkDir(dir string) Option    { return func(o *options) { o.workDir = dir } }
func WithEffort(level string) Option   { return func(o *options) { o.effort = level } }
func WithEnv(env map[string]string) Option { return func(o *options) { o.env = env } }

// WithIncludePartialMessages enables partial message chunks as they arrive.
// Only works with streaming output format.
func WithIncludePartialMessages() Option {
	return func(o *options) { o.includePartialMessages = true }
}

func (o *options) buildArgs() []string {
	return o.buildArgsWithFormat("stream-json")
}

func (o *options) buildBlockingArgs() []string {
	return o.buildArgsWithFormat("json")
}

func (o *options) buildArgsWithFormat(format string) []string {
	args := []string{"--print", "--verbose", "--output-format", format}

	o.appendModelArgs(&args)
	o.appendPromptArgs(&args)
	o.appendToolArgs(&args)
	o.appendOutputArgs(&args)
	o.appendSessionArgs(&args)
	o.appendMCPArgs(&args)
	o.appendAgentArgs(&args)
	o.appendExecArgs(&args)

	return args
}

func (o *options) appendModelArgs(args *[]string) {
	m := o.model
	if m == "" {
		m = DefaultModel
	}
	*args = append(*args, "--model", string(m))

	if o.fallbackModel != "" {
		*args = append(*args, "--fallback-model", string(o.fallbackModel))
	}
}

func (o *options) appendPromptArgs(args *[]string) {
	if o.systemPrompt != "" {
		*args = append(*args, "--system-prompt", o.systemPrompt)
	}
	if o.systemPromptFile != "" {
		*args = append(*args, "--system-prompt-file", o.systemPromptFile)
	}
	if o.appendSystemPrompt != "" {
		*args = append(*args, "--append-system-prompt", o.appendSystemPrompt)
	}
	if o.appendSystemPromptFile != "" {
		*args = append(*args, "--append-system-prompt-file", o.appendSystemPromptFile)
	}
}

func (o *options) appendToolArgs(args *[]string) {
	for _, t := range o.tools {
		*args = append(*args, "--allowedTools", t)
	}
	for _, t := range o.disallowedTools {
		*args = append(*args, "--disallowedTools", t)
	}
	for _, t := range o.builtinTools {
		*args = append(*args, "--tools", t)
	}
	if o.permissionMode != "" {
		*args = append(*args, "--permission-mode", string(o.permissionMode))
	}
}

func (o *options) appendOutputArgs(args *[]string) {
	if o.jsonSchema != "" {
		*args = append(*args, "--json-schema", o.jsonSchema)
	}
	if o.maxBudget > 0 {
		*args = append(*args, "--max-budget-usd", fmt.Sprintf("%.2f", o.maxBudget))
	}
	if o.maxTurns > 0 {
		*args = append(*args, "--max-turns", fmt.Sprintf("%d", o.maxTurns))
	}
	if o.includePartialMessages {
		*args = append(*args, "--include-partial-messages")
	}
}

func (o *options) appendSessionArgs(args *[]string) {
	if o.sessionID != "" {
		*args = append(*args, "--session-id", o.sessionID)
		if o.forkSession {
			*args = append(*args, "--fork-session")
		}
		return
	}
	if o.continueSession {
		*args = append(*args, "--continue")
		return
	}
	*args = append(*args, "--no-session-persistence")
}

func (o *options) appendMCPArgs(args *[]string) {
	for _, c := range o.mcpConfig {
		*args = append(*args, "--mcp-config", c)
	}
	if o.strictMCPConfig {
		*args = append(*args, "--strict-mcp-config")
	}
}

func (o *options) appendAgentArgs(args *[]string) {
	if o.agent != "" {
		*args = append(*args, "--agent", o.agent)
	}
	if o.agentDef != "" {
		*args = append(*args, "--agents", o.agentDef)
	}
}

func (o *options) appendExecArgs(args *[]string) {
	for _, d := range o.addDirs {
		*args = append(*args, "--add-dir", d)
	}
	if o.effort != "" {
		*args = append(*args, "--effort", o.effort)
	}
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
