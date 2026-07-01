package claudecli

// taskIdentity holds the classifying fields the CLI stamps only on a task's
// task_started event: TaskType (e.g. "local_agent", "local_workflow") and,
// for workflows, WorkflowName.
type taskIdentity struct {
	taskType     string
	workflowName string
}

// taskTypeBackfiller remembers each task's identity from its task_started
// event and stamps it back onto the later task_progress/task_updated/
// task_notification events for the same task_id, which the CLI emits with
// task_type == null (verified against claude CLI 2.1.197). Without this,
// TaskEvent.IsWorkflow() and WorkflowName are correct only on task_started —
// notably wrong on the terminal task_notification carrying status "completed".
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
	if ev.TaskType != "" {
		// task_started (the only event that carries task_type): remember it.
		b.byID[ev.TaskID] = taskIdentity{taskType: ev.TaskType, workflowName: ev.WorkflowName}
	} else if id, ok := b.byID[ev.TaskID]; ok {
		// Later event with task_type omitted: restore the classifying fields.
		ev.TaskType = id.taskType
		if ev.WorkflowName == "" {
			ev.WorkflowName = id.workflowName
		}
	}
	// Prune after stamping so the terminal task_notification still gets the
	// backfill before its entry is dropped. Terminal task statuses share the
	// workflow status vocabulary (completed/stopped/killed/failed/error).
	if workflowStatusTerminal(ev.Status) {
		delete(b.byID, ev.TaskID)
	}
	return ev
}
