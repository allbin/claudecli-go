package claudecli

import (
	"encoding/json"
	"slices"
)

// Commands returns the slash commands and skills the session can run now:
// the list from the initialize handshake, replaced by every
// CommandsChangedEvent and every ReloadPlugins answer since. Nil before
// Connect returns, or when the CLI's initialize answer carried no list.
//
// It is the list a client offers when the person types "/": the CLI reports
// every source in it (personal, project and nested skills, command files,
// plugins, claude.ai synced skills and its own built-ins) with descriptions,
// argument hints and aliases, under the names it will resolve. Rebuilding it
// from the skill directories drifts from the CLI's rules.
//
// The CLI watches its skill directories and reports a change as a
// CommandsChangedEvent without being asked, so the list stays current through
// skills added or edited mid-session. Two changes need a reload first, as in
// the terminal: a skills directory that did not exist when the session started
// (ReloadSkills) and a plugin installed mid-session (ReloadPlugins).
//
// The slice is a copy; the caller may keep or change it.
func (s *Session) Commands() []SlashCommand {
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()
	return slices.Clone(s.commands)
}

// setCommands replaces the session's command list with a mid-session change.
func (s *Session) setCommands(cmds []SlashCommand) {
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()
	s.commands = slices.Clone(cmds)
	s.commandsChanged = true
}

// seedCommands takes the list from the initialize answer. It runs on the
// goroutine that waited for that answer while the read loop is already
// delivering events, so a change the read loop applied first is newer and
// wins.
func (s *Session) seedCommands(info json.RawMessage) {
	var body struct {
		Commands []SlashCommand `json:"commands"`
	}
	if json.Unmarshal(info, &body) != nil || body.Commands == nil {
		return
	}
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()
	if !s.commandsChanged {
		s.commands = body.Commands
	}
}
