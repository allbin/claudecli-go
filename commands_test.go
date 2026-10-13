package claudecli

import (
	"context"
	"slices"
	"testing"
)

// The initialize answer as CLI 2.1.296 shapes it: aliases on a plugin skill
// whose bare name is free, and on a skill whose frontmatter renames it.
const initCommands = `{"commands":[` +
	`{"name":"mattpocock-skills:retro","description":"(mattpocock-skills) Conduct a retrospective","argumentHint":"","aliases":["retro"]},` +
	`{"name":"project-bootstrap","description":"Bootstrap (user)","argumentHint":"","aliases":["bootstrap"]},` +
	`{"name":"compact","description":"Compact","argumentHint":"<instructions>","builtin":true}]}`

func TestCommandsSeededFromInitialize(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInitWith(t, initCommands)
		sim.send(`{"type":"system","subtype":"init","session_id":"s1"}`)
		sim.sendResult()
	}()
	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got := session.Commands()
	if len(got) != 3 {
		t.Fatalf("Commands = %+v", got)
	}
	if got[0].Name != "mattpocock-skills:retro" || !slices.Equal(got[0].Aliases, []string{"retro"}) {
		t.Errorf("plugin skill = %+v", got[0])
	}
	if !got[2].Builtin || got[2].ArgumentHint != "<instructions>" {
		t.Errorf("builtin = %+v", got[2])
	}
	// A copy: the caller's edit does not reach the session's list.
	got[0].Name = "changed"
	if session.Commands()[0].Name != "mattpocock-skills:retro" {
		t.Error("Commands returned the session's own slice")
	}
}

func TestCommandsNilWithoutList(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInitWith(t, `{"version":"1.2.3"}`)
		sim.sendResult()
	}()
	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Commands(); got != nil {
		t.Errorf("Commands = %+v, want nil", got)
	}
}

// A commands_changed line replaces the list before the event is delivered,
// so a consumer reacting to it reads the new list.
func TestCommandsReplacedByCommandsChanged(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInitWith(t, initCommands)
		sim.send(`{"type":"system","subtype":"commands_changed","session_id":"s1","commands":[{"name":"probe-new","description":"added mid-session (project)","argumentHint":""}]}`)
		sim.sendResult()
	}()
	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for ev := range session.Events() {
		if _, ok := ev.(*CommandsChangedEvent); ok {
			got := session.Commands()
			if len(got) != 1 || got[0].Name != "probe-new" {
				t.Errorf("Commands at event = %+v", got)
			}
			return
		}
	}
	t.Fatal("no CommandsChangedEvent")
}

func TestCommandsReplacedByReloadPlugins(t *testing.T) {
	var session *Session
	runControl(t, `{"commands":[{"name":"mattpocock-skills:pr","description":"PR body"}],"mcpServers":[],"error_count":0}`,
		func(s *Session) error {
			session = s
			_, err := s.ReloadPlugins()
			return err
		})
	got := session.Commands()
	if len(got) != 1 || got[0].Name != "mattpocock-skills:pr" {
		t.Errorf("Commands = %+v", got)
	}
}

// The initialize answer is applied on the Connect goroutine; a change the
// read loop applied first is newer and must survive it.
func TestSeedCommandsDoesNotOverwriteChange(t *testing.T) {
	s := &Session{}
	s.setCommands([]SlashCommand{{Name: "newer"}})
	s.seedCommands([]byte(initCommands))
	if got := s.Commands(); len(got) != 1 || got[0].Name != "newer" {
		t.Errorf("Commands = %+v", got)
	}
}
