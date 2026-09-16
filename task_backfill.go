package claudecli

// taskIdentity holds what the CLI stamps only on a task's task_started event:
// TaskType (e.g. "local_agent", "local_workflow"), WorkflowName for workflows,
// and OwnedBySubagent. For a workflow it also remembers the agent list from
// the latest progress tree, used to resolve which agent a tick names.
type taskIdentity struct {
	taskType        string
	workflowName    string
	ownedBySubagent bool
	agents          []workflowAgentRef
}

// workflowAgentRef is the part of a workflow_agent progress entry needed to
// resolve a tick's description back to an agent.
type workflowAgentRef struct {
	phaseTitle string
	label      string
	agentID    string
}

// taskTypeBackfiller remembers each task's identity from its task_started
// event and stamps it back onto the later task_progress/task_updated/
// task_notification events for the same task_id, which the CLI emits with
// task_type == null (verified against claude CLI 2.1.197) and without
// owned_by_subagent (CLI 2.1.270). Without this, TaskEvent.IsWorkflow() and
// WorkflowName are correct only on task_started — notably wrong on the
// terminal task_notification carrying status "completed".
//
// It also fills TaskEvent.WorkflowAgentLabel, WorkflowPhaseTitle and
// WorkflowAgentID on workflow task_progress events; see resolveWorkflowAgent.
//
// It is not safe for concurrent use: instantiate one per decode loop and touch
// it only from that loop's goroutine. ParseEvents and Session.readLoop each
// keep their own.
type taskTypeBackfiller struct {
	byID map[string]taskIdentity
}

func newTaskTypeBackfiller() *taskTypeBackfiller {
	return &taskTypeBackfiller{byID: make(map[string]taskIdentity)}
}

// apply restores the classifying fields onto ev when the CLI omitted them,
// records identity carried by ev for later events of the same task, and prunes
// the entry once the task reaches a terminal status. Returns ev for call-site
// convenience. Only the typed convenience fields are touched; ev.Raw is left
// exactly as received.
func (b *taskTypeBackfiller) apply(ev *TaskEvent) *TaskEvent {
	if ev == nil || ev.TaskID == "" {
		return ev
	}
	id, known := b.byID[ev.TaskID]
	if ev.TaskType != "" {
		// task_started (the only event that carries task_type): remember it.
		id = taskIdentity{taskType: ev.TaskType, workflowName: ev.WorkflowName, ownedBySubagent: ev.OwnedBySubagent}
		known = true
	} else if known {
		// Later event with task_type omitted: restore the classifying fields.
		ev.TaskType = id.taskType
		if ev.WorkflowName == "" {
			ev.WorkflowName = id.workflowName
		}
		ev.OwnedBySubagent = ev.OwnedBySubagent || id.ownedBySubagent
	}
	if known && ev.IsWorkflow() && ev.Subtype == "task_progress" {
		if ev.WorkflowProgress != nil {
			id.agents = workflowAgentRefs(ev.WorkflowProgress)
		}
		resolveWorkflowAgent(ev, id.agents)
	}
	if known {
		b.byID[ev.TaskID] = id
	}
	// Prune after stamping so the terminal task_notification still gets the
	// backfill before its entry is dropped. Terminal task statuses share the
	// workflow status vocabulary (completed/stopped/killed/failed/error).
	if workflowStatusTerminal(ev.Status) {
		delete(b.byID, ev.TaskID)
	}
	return ev
}

func workflowAgentRefs(progress []WorkflowProgressEntry) []workflowAgentRef {
	refs := make([]workflowAgentRef, 0, len(progress))
	for _, p := range progress {
		if p.IsAgent() {
			refs = append(refs, workflowAgentRef{phaseTitle: p.PhaseTitle, label: p.Label, agentID: p.AgentID})
		}
	}
	return refs
}

// resolveWorkflowAgent names the agent a workflow task_progress tick is about.
//
// The CLI sends that agent in "description" as "<phaseTitle>: <label>", or as
// the bare label when the agent has no phase (CLI 2.1.270). Splitting on ": "
// is ambiguous, since labels and titles may contain it, so the description is
// matched against the agents of the latest progress tree instead. The CLI
// sends a tree when an agent is queued or started, so every agent is known
// before its first usage tick. A description matching no agent, or two
// different (phase, label) pairs, leaves the fields empty. WorkflowAgentID is
// set only when every matching entry carries the same non-empty agent id; a
// queued agent has none yet.
func resolveWorkflowAgent(ev *TaskEvent, agents []workflowAgentRef) {
	if ev.Description == "" {
		return
	}
	var match *workflowAgentRef
	agentID := ""
	for i := range agents {
		a := &agents[i]
		name := a.label
		if a.phaseTitle != "" {
			name = a.phaseTitle + ": " + a.label
		}
		if name != ev.Description {
			continue
		}
		if match == nil {
			match, agentID = a, a.agentID
			continue
		}
		if a.phaseTitle != match.phaseTitle || a.label != match.label {
			return // ambiguous
		}
		if a.agentID != agentID {
			agentID = ""
		}
	}
	if match == nil {
		return
	}
	ev.WorkflowPhaseTitle = match.phaseTitle
	ev.WorkflowAgentLabel = match.label
	ev.WorkflowAgentID = agentID
}
