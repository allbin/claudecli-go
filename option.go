package claudecli

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Option configures a Run call. Options set at call time
// replace (not merge with) client-level defaults.
type Option func(*options)

type options struct {
	// client-only
	binaryPath string

	// model
	model             Model
	fallbackModel     Model
	betas             []string
	maxThinkingTokens int

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
	permissionMode           PermissionMode
	permissionPromptToolName string

	// output
	jsonSchema string

	// budget and limits
	maxBudget float64
	maxTurns  int

	// session
	sessionID       string
	forkSession     bool
	continueSession bool
	resume          string

	// MCP
	mcpConfig       []string
	strictMCPConfig bool

	// agents
	agent    string
	agentDef string

	// settings
	settings       string
	settingSources []string

	// plugins
	pluginDirs []string

	// execution
	timeout                 time.Duration
	addDirs                 []string
	workDir                 string
	effort                  EffortLevel
	env                     map[string]string
	includePartialMessages  bool
	extraArgs               map[string]string
	user                    string
	stderrCallback          func(string)
	enableFileCheckpointing bool

	// session callbacks
	canUseTool     ToolPermissionFunc
	controlTimeout time.Duration // timeout for control request responses

	// version check
	skipVersionCheck bool
}

// WithBinaryPath sets the Claude CLI binary path. Only effective when passed
// to New() (ignored at call time). Defaults to "claude".
func WithBinaryPath(path string) Option {
	return func(o *options) { o.binaryPath = path }
}

func WithModel(m Model) Option           { return func(o *options) { o.model = m } }
func WithFallbackModel(m Model) Option   { return func(o *options) { o.fallbackModel = m } }
func WithBetas(betas ...string) Option   { return func(o *options) { o.betas = betas } }
func WithMaxThinkingTokens(n int) Option { return func(o *options) { o.maxThinkingTokens = n } }

func WithSystemPrompt(p string) Option     { return func(o *options) { o.systemPrompt = p } }
func WithSystemPromptFile(p string) Option { return func(o *options) { o.systemPromptFile = p } }
func WithAppendSystemPrompt(p string) Option {
	return func(o *options) { o.appendSystemPrompt = p }
}
func WithAppendSystemPromptFile(p string) Option {
	return func(o *options) { o.appendSystemPromptFile = p }
}

// WithTools sets allowed tools. Accepts individual names or comma-separated lists.
// Both WithTools("A", "B") and WithTools("A,B") produce one --allowedTools per tool.
func WithTools(tools ...string) Option {
	return func(o *options) { o.tools = normalizeTools(tools) }
}

// WithDisallowedTools sets disallowed tools. Accepts individual names or comma-separated lists.
func WithDisallowedTools(tools ...string) Option {
	return func(o *options) { o.disallowedTools = normalizeTools(tools) }
}

// WithBuiltinTools restricts which built-in tools are available.
// Use "default" for all tools, "" to disable all, or specific names like "Bash", "Edit", "Read".
// Different from WithTools which controls permission prompts — this controls tool availability.
func WithBuiltinTools(tools ...string) Option { return func(o *options) { o.builtinTools = tools } }

func WithPermissionMode(m PermissionMode) Option { return func(o *options) { o.permissionMode = m } }
func WithPermissionPromptToolName(name string) Option {
	return func(o *options) { o.permissionPromptToolName = name }
}
func WithJSONSchema(schema string) Option    { return func(o *options) { o.jsonSchema = schema } }
func WithMaxBudget(usd float64) Option       { return func(o *options) { o.maxBudget = usd } }
func WithMaxTurns(n int) Option              { return func(o *options) { o.maxTurns = n } }
func WithSessionID(id string) Option         { return func(o *options) { o.sessionID = id } }
func WithForkSession() Option                { return func(o *options) { o.forkSession = true } }
func WithContinue() Option                   { return func(o *options) { o.continueSession = true } }
func WithMCPConfig(configs ...string) Option { return func(o *options) { o.mcpConfig = configs } }
func WithStrictMCPConfig() Option            { return func(o *options) { o.strictMCPConfig = true } }

// WithAgent selects a named agent for the session.
func WithAgent(name string) Option { return func(o *options) { o.agent = name } }

// WithAgentDef defines custom agents via a JSON string.
// Example: `{"reviewer": {"description": "Reviews code", "prompt": "You are a code reviewer"}}`.
func WithAgentDef(jsonDef string) Option { return func(o *options) { o.agentDef = jsonDef } }

// WithAddDirs adds directories the CLI tools can access beyond the working directory.
func WithAddDirs(dirs ...string) Option { return func(o *options) { o.addDirs = dirs } }

func WithSettings(s string) Option { return func(o *options) { o.settings = s } }
func WithSettingSources(sources ...string) Option {
	return func(o *options) { o.settingSources = sources }
}
func WithPluginDirs(dirs ...string) Option        { return func(o *options) { o.pluginDirs = dirs } }
func WithWorkDir(dir string) Option               { return func(o *options) { o.workDir = dir } }
func WithEffort(level EffortLevel) Option         { return func(o *options) { o.effort = level } }
func WithEnv(env map[string]string) Option        { return func(o *options) { o.env = env } }
func WithResume(sessionID string) Option          { return func(o *options) { o.resume = sessionID } }
func WithExtraArgs(args map[string]string) Option { return func(o *options) { o.extraArgs = args } }
func WithUser(user string) Option                 { return func(o *options) { o.user = user } }
func WithTimeout(d time.Duration) Option          { return func(o *options) { o.timeout = d } }
func WithStderrCallback(fn func(string)) Option   { return func(o *options) { o.stderrCallback = fn } }
func WithFileCheckpointing() Option               { return func(o *options) { o.enableFileCheckpointing = true } }
func WithSkipVersionCheck() Option                { return func(o *options) { o.skipVersionCheck = true } }

// WithCanUseTool registers a callback for tool permission requests.
// Only effective with Connect() sessions.
func WithCanUseTool(fn ToolPermissionFunc) Option {
	return func(o *options) { o.canUseTool = fn }
}

// WithControlTimeout sets the timeout for control protocol request/response
// round-trips (e.g. initialize, set_model, mcp operations). Defaults to 30s.
// Only effective with Connect() sessions.
func WithControlTimeout(d time.Duration) Option {
	return func(o *options) { o.controlTimeout = d }
}

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
	o.appendSettingsArgs(&args)
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
	if len(o.betas) > 0 {
		*args = append(*args, "--betas", strings.Join(o.betas, ","))
	}
	if o.maxThinkingTokens > 0 {
		*args = append(*args, "--max-thinking-tokens", fmt.Sprintf("%d", o.maxThinkingTokens))
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
	if o.resume != "" {
		*args = append(*args, "--resume", o.resume)
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

func (o *options) appendSettingsArgs(args *[]string) {
	if o.settings != "" {
		*args = append(*args, "--settings", o.settings)
	}
	if len(o.settingSources) > 0 {
		*args = append(*args, "--setting-sources", strings.Join(o.settingSources, ","))
	}
	for _, d := range o.pluginDirs {
		*args = append(*args, "--plugin-dir", d)
	}
}

func (o *options) appendExecArgs(args *[]string) {
	// Note: --timeout is not a valid Claude CLI flag. Callers should use
	// context.WithTimeout instead. WithTimeout is kept for backward compat
	// but no longer emits a CLI argument.
	for _, d := range o.addDirs {
		*args = append(*args, "--add-dir", d)
	}
	if o.effort != "" {
		*args = append(*args, "--effort", string(o.effort))
	}
	if o.user != "" {
		*args = append(*args, "--user", o.user)
	}
	keys := make([]string, 0, len(o.extraArgs))
	for k := range o.extraArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		*args = append(*args, "--"+k)
		if v := o.extraArgs[k]; v != "" {
			*args = append(*args, v)
		}
	}
}

func (o *options) buildSessionArgs() []string {
	args := []string{"--verbose", "--output-format", "stream-json", "--input-format", "stream-json"}

	o.appendModelArgs(&args)
	o.appendPromptArgs(&args)
	o.appendToolArgs(&args)
	o.appendOutputArgs(&args)

	// Session mode: skip --no-session-persistence, keep session/continue flags
	if o.sessionID != "" {
		args = append(args, "--session-id", o.sessionID)
		if o.forkSession {
			args = append(args, "--fork-session")
		}
	} else if o.resume != "" {
		args = append(args, "--resume", o.resume)
	} else if o.continueSession {
		args = append(args, "--continue")
	}

	o.appendMCPArgs(&args)
	o.appendAgentArgs(&args)
	o.appendSettingsArgs(&args)
	o.appendExecArgs(&args)

	if o.canUseTool != nil {
		toolName := "stdio"
		if o.permissionPromptToolName != "" {
			toolName = o.permissionPromptToolName
		}
		args = append(args, "--permission-prompt-tool", toolName)
	}

	return args
}

// normalizeTools splits comma-separated tool names, trims whitespace, and deduplicates.
func normalizeTools(tools []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, t := range tools {
		for _, name := range strings.Split(t, ",") {
			name = strings.TrimSpace(name)
			if name != "" && !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	return result
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
