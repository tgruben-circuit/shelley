package cluster

import (
	"context"
	"fmt"
)

// PlannedTask is a task bundled with its dependency list.
type PlannedTask struct {
	Task      Task     `json:"task"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// TaskPlan is an ordered list of tasks with dependency edges.
type TaskPlan struct {
	Tasks []PlannedTask `json:"tasks"`
}

// Orchestrator manages a task plan with dependency tracking. It submits
// tasks to the node's TaskQueue as their dependencies are satisfied.
type Orchestrator struct {
	node          *Node
	plan          *TaskPlan
	submitted     map[string]bool // task IDs already submitted to the queue
	workingBranch string
}

// SetWorkingBranch records the branch that worker branches merge into.
func (o *Orchestrator) SetWorkingBranch(branch string) {
	o.workingBranch = branch
}

// WorkingBranch returns the configured working branch.
func (o *Orchestrator) WorkingBranch() string {
	return o.workingBranch
}

// NewOrchestrator creates an Orchestrator tied to the given cluster node.
func NewOrchestrator(node *Node) *Orchestrator {
	return &Orchestrator{
		node:      node,
		submitted: make(map[string]bool),
	}
}

// SubmitPlan stores the plan and immediately submits all tasks that have no
// dependencies. Each submitted task gets its CreatedBy set to the node's
// agent ID.
func (o *Orchestrator) SubmitPlan(ctx context.Context, plan TaskPlan) error {
	o.plan = &plan

	for _, pt := range plan.Tasks {
		if len(pt.DependsOn) == 0 {
			task := pt.Task
			task.DependsOn = pt.DependsOn
			if err := o.submitTask(ctx, task); err != nil {
				return fmt.Errorf("submit plan: %w", err)
			}
		}
	}
	return nil
}

// ResolveDependencies checks for plan tasks whose dependencies are all
// completed and that have not already been submitted. It submits them and
// returns the newly unblocked tasks. The method is idempotent: calling it
// multiple times without new completions produces no duplicates.
func (o *Orchestrator) ResolveDependencies(ctx context.Context) ([]Task, error) {
	if o.plan == nil {
		return nil, nil
	}

	completed, err := o.completedSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve dependencies: %w", err)
	}

	var unblocked []Task
	for _, pt := range o.plan.Tasks {
		if len(pt.DependsOn) == 0 {
			continue // already handled by SubmitPlan
		}
		if o.submitted[pt.Task.ID] {
			continue // already submitted
		}
		if !allIn(pt.DependsOn, completed) {
			continue // not all deps satisfied
		}

		task := pt.Task
		task.DependsOn = pt.DependsOn
		if err := o.submitTask(ctx, task); err != nil {
			return nil, fmt.Errorf("resolve dependencies: %w", err)
		}
		unblocked = append(unblocked, task)
	}
	return unblocked, nil
}

// PendingTasks returns plan tasks that have dependencies (i.e. tasks that
// were not immediately submitted by SubmitPlan).
func (o *Orchestrator) PendingTasks() []PlannedTask {
	if o.plan == nil {
		return nil
	}
	var pending []PlannedTask
	for _, pt := range o.plan.Tasks {
		if len(pt.DependsOn) > 0 {
			pending = append(pending, pt)
		}
	}
	return pending
}

// submitTask sets CreatedBy and submits the task to the node's queue.
func (o *Orchestrator) submitTask(ctx context.Context, task Task) error {
	task.CreatedBy = o.node.Config.AgentID
	if err := o.node.Tasks.Submit(ctx, task); err != nil {
		return err
	}
	o.submitted[task.ID] = true
	return nil
}

// completedSet builds a set of task IDs that are currently completed.
func (o *Orchestrator) completedSet(ctx context.Context) (map[string]bool, error) {
	tasks, err := o.node.Tasks.ListByStatus(ctx, TaskStatusCompleted)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		set[t.ID] = true
	}
	return set, nil
}

// allIn returns true if every element of ids is present in the set.
func allIn(ids []string, set map[string]bool) bool {
	for _, id := range ids {
		if !set[id] {
			return false
		}
	}
	return true
}
