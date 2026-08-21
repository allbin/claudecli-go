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
	model         Model
	fallbackModel Model
	betas         []string

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
	sessionName     string
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
	pluginURLs []string

	// execution
	timeout                            time.Duration
	addDirs                            []string
	workDir                            string
	effort                             EffortLevel
	thinking                           ThinkingConfig
	taskBudget                         int
	env                                map[string]string
	includePartialMessages             bool
	extraArgs                          map[string]string
	stderrCallback                     func(string)
	enableFileCheckpointing            bool
	bare                               bool
	replayUserMessages                 bool
	dangerouslySkipPerms               bool
	disableSlashCommands               bool
	debugFile                          string
	safeMode                           bool
	autoCompact                        string
	excludeDynamicSystemPromptSections bool

	// stream-json-only output toggles
	includeHookEvents   bool
	forwardSubagentText bool
	promptSuggestions   bool

	// session callbacks
	canUseTool        ToolPermissionFunc
	canUseToolReq     ToolPermissionRequestFunc
	canUseToolReqCtx  ToolPermissionRequestContextFunc
	userInput         UserInputFunc
	userInputCtx      UserInputContextFunc
	controlTimeout    time.Duration // timeout for control request responses
	initTimeout       time.Duration // timeout for initialize handshake (includes MCP startup)
	stdinWriteTimeout time.Duration // deadline for individual stdin writes (session)

	// version check
	skipVersionCheck bool
}

// WithBinaryPath sets the Claude CLI binary path. Only effective when passed
// to New() (ignored at call time). Defaults to "claude".
func WithBinaryPath(path string) Option {
	return func(o *options) { o.binaryPath = path }
}

func WithModel(m Model) Option         { return func(o *options) { o.model = m } }
func WithFallbackModel(m Model) Option { return func(o *options) { o.fallbackModel = m } }
func WithBetas(betas ...string) Option { return func(o *options) { o.betas = betas } }

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
func WithPluginDirs(dirs ...string) Option { return func(o *options) { o.pluginDirs = dirs } }
func WithWorkDir(dir string) Option        { return func(o *options) { o.workDir = dir } }
func WithEffort(level EffortLevel) Option  { return func(o *options) { o.effort = level } }

// WithThinking configures extended thinking mode directly, emitting the
// CLI's --thinking or --max-thinking-tokens flag. Use ThinkingAdaptive{},
// ThinkingEnabled{BudgetTokens: N}, or ThinkingDisabled{}.
//
// Mutually overlapping with WithEffort — if both are set the CLI receives
// both flags. Prefer WithEffort for normal use; WithThinking is for
// callers that need explicit budget control or to force thinking off.
func WithThinking(cfg ThinkingConfig) Option {
	return func(o *options) { o.thinking = cfg }
}

// WithTaskBudget caps the total tokens a task may consume. Emits
// --task-budget to the CLI. Zero or negative values are ignored.
func WithTaskBudget(totalTokens int) Option {
	return func(o *options) { o.taskBudget = totalTokens }
}
func WithEnv(env map[string]string) Option { return func(o *options) { o.env = env } }
func WithResume(sessionID string) Option   { return func(o *options) { o.resume = sessionID } }

// WithExtraArgs passes additional CLI flags. Keys are flag names without the
// leading "--". Flags managed by the SDK (print, output-format, input-format,
// verbose, model) are rejected with a panic to prevent conflicting arguments.
func WithExtraArgs(args map[string]string) Option {
	for k := range args {
		if reservedFlags[k] {
			panic(fmt.Sprintf("claudecli: WithExtraArgs: %q is a reserved flag managed by the SDK", k))
		}
	}
	return func(o *options) { o.extraArgs = args }
}

// WithBare enables minimal mode: skip hooks, LSP, plugin sync, attribution,
// auto-memory, background prefetches, keychain reads, and CLAUDE.md auto-discovery.
func WithBare() Option { return func(o *options) { o.bare = true } }

// WithReplayUserMessages causes the CLI to echo user messages back on stdout
// after reading them from stdin. The echoed messages appear as UserEvent with
// IsReplay=true, confirming message delivery. Only works with interactive
// sessions (Connect) which use stream-json I/O.
func WithReplayUserMessages() Option {
	return func(o *options) { o.replayUserMessages = true }
}

// WithDangerouslySkipPermissions bypasses all permission checks.
// Emits both --allow-dangerously-skip-permissions and --dangerously-skip-permissions.
// Only use in sandboxed environments with no internet access.
func WithDangerouslySkipPermissions() Option {
	return func(o *options) { o.dangerouslySkipPerms = true }
}
func WithSessionName(name string) Option { return func(o *options) { o.sessionName = name } }
func WithDebugFile(path string) Option   { return func(o *options) { o.debugFile = path } }
func WithDisableSlashCommands() Option   { return func(o *options) { o.disableSlashCommands = true } }

// WithUser is a no-op.
//
// Deprecated: the CLI removed --user; passing it makes the CLI exit with
// "unknown option '--user'". This option no longer emits any argument and is
// kept only so existing callers still compile. It will be removed in a future
// release.
func WithUser(string) Option { return func(*options) {} }

// WithSafeMode starts the CLI with all customizations disabled — CLAUDE.md,
// skills, plugins, hooks, MCP servers, custom commands and agents, output
// styles, and workflows. Admin-managed policy settings still apply, and auth,
// model selection, built-in tools, and permissions work normally. Emits
// --safe-mode. Useful for reproducing behavior without local configuration.
func WithSafeMode() Option { return func(o *options) { o.safeMode = true } }

// WithAutoCompact sets the auto-compact window size. Pass "auto" to let the
// CLI choose, or a token count between 100k and 1M (e.g. "200000"). Emits
// --autocompact.
func WithAutoCompact(window string) Option { return func(o *options) { o.autoCompact = window } }

// WithExcludeDynamicSystemPromptSections moves per-machine sections (cwd, env
// info, memory paths, git status) out of the system prompt and into the first
// user message, which improves prompt-cache reuse across machines and users.
// Emits --exclude-dynamic-system-prompt-sections. The CLI ignores it when a
// custom system prompt is set via WithSystemPrompt.
func WithExcludeDynamicSystemPromptSections() Option {
	return func(o *options) { o.excludeDynamicSystemPromptSections = true }
}

// WithPluginURLs fetches plugin .zip archives from URLs for this session only.
// Emits one --plugin-url per URL.
func WithPluginURLs(urls ...string) Option { return func(o *options) { o.pluginURLs = urls } }

// WithIncludeHookEvents makes the CLI report hook lifecycle activity on the
// event stream, surfacing *HookEvent values (hook_started, hook_progress,
// hook_response). Without this option the CLI emits no hook events at all.
//
// Only valid for streaming runs (Run and Session). RunBlocking ignores it,
// because the CLI rejects the flag when the output format is plain JSON.
func WithIncludeHookEvents() Option { return func(o *options) { o.includeHookEvents = true } }

// WithForwardSubagentText forwards text and thinking blocks produced by
// subagents as *TextEvent and *ThinkingEvent values with ParentToolUseID set
// to the spawning Agent tool_use ID. Without it, a subagent's interior output
// is invisible and only its final result appears. Nested subagents (depth 2+)
// are forwarded as well, each keyed by its own spawning tool_use ID.
//
// Only valid for streaming runs (Run and Session). RunBlocking ignores it,
// because the CLI rejects the flag when the output format is plain JSON.
func WithForwardSubagentText() Option { return func(o *options) { o.forwardSubagentText = true } }

// WithPromptSuggestions asks the CLI to predict a plausible next user prompt,
// emitted as a *PromptSuggestionEvent after each turn.
//
// Only Session delivers the event. The CLI emits prompt_suggestion after the
// turn's result message, and a one-shot Run stops at that result — the
// terminal event — so the suggestion never arrives there. RunBlocking omits
// the flag entirely, because the CLI rejects it when the output format is
// plain JSON.
func WithPromptSuggestions() Option             { return func(o *options) { o.promptSuggestions = true } }
func WithTimeout(d time.Duration) Option        { return func(o *options) { o.timeout = d } }
func WithStderrCallback(fn func(string)) Option { return func(o *options) { o.stderrCallback = fn } }
func WithFileCheckpointing() Option             { return func(o *options) { o.enableFileCheckpointing = true } }
func WithSkipVersionCheck() Option              { return func(o *options) { o.skipVersionCheck = true } }

// WithCanUseTool registers a callback for tool permission requests.
// Only effective with Connect() sessions.
//
// The callback runs in a goroutine and must return promptly. If the session's
// context is cancelled (e.g. via Close), the SDK stops waiting for the callback
// but cannot forcibly terminate it. A callback that blocks indefinitely will
// leak its goroutine. Long-running callbacks should select on ctx.Done().
func WithCanUseTool(fn ToolPermissionFunc) Option {
	return func(o *options) { o.canUseTool = fn }
}

// WithCanUseToolRequest registers a tool-permission callback that receives the
// full ToolPermissionRequest instead of just the tool name and input.
//
// Use this over WithCanUseTool when the host needs to attribute the prompt
// (ToolUseID, AgentID), explain it (DecisionReason, DecisionReasonType), or
// support "always allow" (PermissionSuggestions, echoed back via
// PermissionResponse.UpdatedPermissions).
//
// Takes precedence over WithCanUseTool if both are set. Also adds
// --permission-prompt-tool.
func WithCanUseToolRequest(fn ToolPermissionRequestFunc) Option {
	return func(o *options) { o.canUseToolReq = fn }
}

// WithCanUseToolRequestContext registers a tool-permission callback that
// receives a per-request context alongside the full ToolPermissionRequest.
//
// Use this over WithCanUseToolRequest when the callback parks the decision on a
// human, which is the normal shape of an interactive permission dialog. The
// context is the only way to learn that the request was withdrawn:
//
//   - ctx is cancelled when the CLI sends control_cancel_request for this
//     request id — its turn was interrupted, or another client answered first —
//     and when the session context ends.
//   - Once cancelled, the callback's return value is discarded: the SDK writes
//     no control_response for that request id, and the CLI has stopped waiting
//     for one.
//   - A host must therefore treat ctx.Done() as "drop the prompt": take the
//     dialog off screen and release whatever is blocked on the user, instead of
//     waiting for an answer that can no longer be delivered.
//
// Precedence follows the existing rule — the more informed callback wins. This
// one beats WithCanUseToolRequest, which beats WithCanUseTool. Also adds
// --permission-prompt-tool.
func WithCanUseToolRequestContext(fn ToolPermissionRequestContextFunc) Option {
	return func(o *options) { o.canUseToolReqCtx = fn }
}

// WithUserInput registers a callback for AskUserQuestion tool requests.
// Only effective with Connect() sessions.
//
// When registered, AskUserQuestion requests route here instead of the
// ToolPermissionFunc callback. Other tool permission requests are unaffected.
// Also adds --permission-prompt-tool (same as WithCanUseTool).
//
// The callback cannot observe a withdrawn request. Use
// WithUserInputContext when that matters — for a question put to a user, it
// usually does.
func WithUserInput(fn UserInputFunc) Option {
	return func(o *options) { o.userInput = fn }
}

// WithUserInputContext registers an AskUserQuestion callback that receives a
// per-request context alongside the questions.
//
// The cancellation contract is the same as WithCanUseToolRequestContext: ctx is
// cancelled when the CLI withdraws the request (inbound control_cancel_request
// for this request id) or when the session ends, and from that point the
// answers are discarded and no control_response is written. Take the question
// off screen when ctx.Done() fires rather than keep waiting on the user.
//
// Takes precedence over WithUserInput if both are set. Also adds
// --permission-prompt-tool (same as WithCanUseTool).
func WithUserInputContext(fn UserInputContextFunc) Option {
	return func(o *options) { o.userInputCtx = fn }
}

// WithControlTimeout sets the timeout for control protocol request/response
// round-trips (e.g. set_model, mcp operations). Defaults to 30s.
// Does not affect the initialize handshake — use WithInitTimeout for that.
// Only effective with Connect() sessions.
func WithControlTimeout(d time.Duration) Option {
	return func(o *options) { o.controlTimeout = d }
}

// WithInitTimeout sets the timeout for the initialize handshake during
// Connect(). This is separate from WithControlTimeout because initialization
// can be slow when the CLI is connecting to MCP servers. Defaults to 60s.
// Only effective with Connect() sessions.
func WithInitTimeout(d time.Duration) Option {
	return func(o *options) { o.initTimeout = d }
}

// WithStdinWriteTimeout sets the deadline for individual stdin writes to the
// CLI process. A write that blocks longer than this means the CLI has stopped
// reading stdin (hung child, full pipe): the write fails, stdin is closed and
// the session must be recycled. Without a deadline a blocked write holds the
// session's write lock forever and Close() deadlocks against it.
// Defaults to 30s. Only effective with Connect() sessions.
func WithStdinWriteTimeout(d time.Duration) Option {
	return func(o *options) { o.stdinWriteTimeout = d }
}

// WithIncludePartialMessages enables partial message chunks as they arrive.
// Only works with streaming output format.
func WithIncludePartialMessages() Option {
	return func(o *options) { o.includePartialMessages = true }
}

// buildCommonArgs returns flags shared by all three builders:
// model, prompts, tools, output, MCP, agents, settings, exec.
// Does NOT include --print, --output-format, --input-format, --verbose,
// or session/permission flags — those are mode-specific.
func (o *options) buildCommonArgs() []string {
	var args []string

	o.appendModelArgs(&args)
	o.appendPromptArgs(&args)
	o.appendToolArgs(&args)
	o.appendOutputArgs(&args)
	o.appendMCPArgs(&args)
	o.appendAgentArgs(&args)
	o.appendSettingsArgs(&args)
	o.appendExecArgs(&args)

	return args
}

func (o *options) buildArgs() []string {
	args := []string{"--print", "--verbose", "--output-format", "stream-json"}
	args = append(args, o.buildCommonArgs()...)
	o.appendStreamOnlyArgs(&args)
	o.appendSessionArgs(&args)
	return args
}

func (o *options) buildBlockingArgs() []string {
	args := []string{"--print", "--verbose", "--output-format", "json"}
	args = append(args, o.buildCommonArgs()...)
	o.appendSessionArgs(&args)
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
	if o.dangerouslySkipPerms {
		*args = append(*args, "--allow-dangerously-skip-permissions", "--dangerously-skip-permissions")
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
	if o.sessionName != "" {
		*args = append(*args, "--name", o.sessionName)
	}
	if o.sessionID != "" {
		*args = append(*args, "--session-id", o.sessionID)
		if o.forkSession {
			*args = append(*args, "--fork-session")
		}
		return
	}
	if o.resume != "" {
		*args = append(*args, "--resume", o.resume)
		if o.forkSession {
			*args = append(*args, "--fork-session")
		}
		return
	}
	if o.continueSession {
		*args = append(*args, "--continue")
		if o.forkSession {
			*args = append(*args, "--fork-session")
		}
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
	for _, u := range o.pluginURLs {
		*args = append(*args, "--plugin-url", u)
	}
}

// appendStreamOnlyArgs emits flags the CLI accepts only when the output format
// is stream-json. They are omitted from buildBlockingArgs (--output-format
// json), where the CLI rejects them outright.
func (o *options) appendStreamOnlyArgs(args *[]string) {
	if o.includeHookEvents {
		*args = append(*args, "--include-hook-events")
	}
	if o.forwardSubagentText {
		*args = append(*args, "--forward-subagent-text")
	}
	if o.promptSuggestions {
		// The value is passed explicitly: --prompt-suggestions takes an
		// optional value, so a bare flag would swallow a following
		// positional argument.
		*args = append(*args, "--prompt-suggestions", "true")
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
	if o.thinking != nil {
		o.thinking.appendArgs(args)
	}
	if o.taskBudget > 0 {
		*args = append(*args, "--task-budget", fmt.Sprintf("%d", o.taskBudget))
	}
	if o.bare {
		*args = append(*args, "--bare")
	}
	if o.debugFile != "" {
		*args = append(*args, "--debug-file", o.debugFile)
	}
	if o.disableSlashCommands {
		*args = append(*args, "--disable-slash-commands")
	}
	if o.safeMode {
		*args = append(*args, "--safe-mode")
	}
	if o.autoCompact != "" {
		*args = append(*args, "--autocompact", o.autoCompact)
	}
	if o.excludeDynamicSystemPromptSections {
		*args = append(*args, "--exclude-dynamic-system-prompt-sections")
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
	args = append(args, o.buildCommonArgs()...)
	o.appendStreamOnlyArgs(&args)

	// Session mode: skip --no-session-persistence, keep session/continue flags
	if o.sessionName != "" {
		args = append(args, "--name", o.sessionName)
	}
	if o.sessionID != "" {
		args = append(args, "--session-id", o.sessionID)
		if o.forkSession {
			args = append(args, "--fork-session")
		}
	} else if o.resume != "" {
		args = append(args, "--resume", o.resume)
		if o.forkSession {
			args = append(args, "--fork-session")
		}
	} else if o.continueSession {
		args = append(args, "--continue")
		if o.forkSession {
			args = append(args, "--fork-session")
		}
	}

	if o.canUseTool != nil || o.canUseToolReq != nil || o.canUseToolReqCtx != nil ||
		o.userInput != nil || o.userInputCtx != nil {
		toolName := "stdio"
		if o.permissionPromptToolName != "" {
			toolName = o.permissionPromptToolName
		}
		args = append(args, "--permission-prompt-tool", toolName)
	}

	if o.replayUserMessages {
		args = append(args, "--replay-user-messages")
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

// reservedFlags are CLI flags managed by the SDK that must not be
// overridden via WithExtraArgs to prevent undefined behavior.
var reservedFlags = map[string]bool{
	"print":         true,
	"output-format": true,
	"input-format":  true,
	"verbose":       true,
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

// ResolveCanUseTool applies the given options and returns the ToolPermissionFunc
// callback, or nil if none was set. Used by test infrastructure to extract
// callbacks that would normally be consumed internally by Connect().
func ResolveCanUseTool(opts ...Option) ToolPermissionFunc {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return o.canUseTool
}
