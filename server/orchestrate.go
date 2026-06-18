package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tgruben-circuit/percy/claudetool"
)

// Stage->model defaults. These are the REAL percy model IDs declared in
// models/models.go. Do not use the placeholder literals from the task
// description ("claude-opus-4-8", "accounts/fireworks/models/glm-5p2") —
// they do not resolve and resolveModelID would reject them.
const (
	defaultPlannerModel  = "claude-opus-4.8"
	defaultBuilderModel  = "glm-4p6-fireworks"
	defaultVerifierModel = "gpt-5.5"
)

// Plan is the JSON plan emitted by the planner stage, matching the pi
// orchestration schema: goal + batches, each batch having an id, mode
// (parallel|sequential), and a list of tasks.
type Plan struct {
	Goal    string  `json:"goal"`
	Batches []Batch `json:"batches"`
}

// Batch groups tasks that should be executed together.
type Batch struct {
	ID    string `json:"id"`
	Mode  string `json:"mode"`
	Tasks []Task `json:"tasks"`
}

// Task is a single unit of builder work.
type Task struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

// orchestrateOptions controls per-stage model selection, concurrency, and
// whether the verifier stage runs.
type orchestrateOptions struct {
	PlannerModel   string
	BuilderModel   string
	VerifierModel  string
	MaxConcurrency int
	SkipVerify     bool
}

// orchestrateResult is the full result of an orchestration run, returned to
// the HTTP caller as JSON.
type orchestrateResult struct {
	Goal     string         `json:"goal"`
	RawPlan  string         `json:"raw_plan"`
	Plan     Plan           `json:"plan"`
	Batches  []batchResult  `json:"batches"`
	Verifier verifierResult `json:"verifier"`
	Errors   []string       `json:"errors,omitempty"`
}

// batchResult is the outcome of one batch.
type batchResult struct {
	ID    string       `json:"id"`
	Mode  string       `json:"mode"`
	Tasks []taskResult `json:"tasks"`
}

// taskResult is the outcome of one builder task.
type taskResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result string `json:"result"`
}

// verifierResult is the outcome of the verifier stage.
type verifierResult struct {
	Skipped bool   `json:"skipped,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// parsePlan extracts a Plan from the planner's raw text output.
//
// It first looks for the first ```json fenced block; if none is present it
// scans from the first '{' to the last '}'. It then json.Unmarshals into
// Plan. There is NO silent fallback: an unmarshal failure, an empty Batches
// list, or any batch whose Mode is not exactly "parallel" or "sequential"
// returns a descriptive error (AGENTS.md rule 3).
func parsePlan(raw string) (Plan, error) {
	extracted := extractPlanJSON(raw)
	if strings.TrimSpace(extracted) == "" {
		return Plan{}, fmt.Errorf("orchestrate: planner output contained no JSON plan")
	}

	var plan Plan
	if err := json.Unmarshal([]byte(extracted), &plan); err != nil {
		return Plan{}, fmt.Errorf("orchestrate: failed to parse plan JSON: %w", err)
	}
	if len(plan.Batches) == 0 {
		return Plan{}, fmt.Errorf("orchestrate: plan has no batches")
	}
	for i, b := range plan.Batches {
		if b.Mode != "parallel" && b.Mode != "sequential" {
			return Plan{}, fmt.Errorf("orchestrate: batch %d (%q) has invalid mode %q; want \"parallel\" or \"sequential\"", i, b.ID, b.Mode)
		}
	}
	return plan, nil
}

// extractPlanJSON returns the JSON text to unmarshal: the contents of the
// first ```json fenced block if present, otherwise the substring from the
// first '{' to the last '}'.
func extractPlanJSON(raw string) string {
	if block := firstFencedJSON(raw); block != "" {
		return block
	}
	first := strings.IndexByte(raw, '{')
	last := strings.LastIndexByte(raw, '}')
	if first >= 0 && last > first {
		return raw[first : last+1]
	}
	return ""
}

// firstFencedJSON returns the contents of the first ```json fenced code
// block, or "" if there is none.
func firstFencedJSON(raw string) string {
	idx := strings.Index(raw, "```json")
	if idx < 0 {
		// Also tolerate a bare ``` fence that happens to be JSON.
		return ""
	}
	rest := raw[idx+len("```json"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// resolveOrchestrateModel applies the precedence
// request-body override > const default, then resolves via resolveModelID.
func (s *Server) resolveOrchestrateModel(requested, def string) (string, error) {
	m := requested
	if m == "" {
		m = def
	}
	resolved, err := s.resolveModelID(m)
	if err != nil {
		return "", fmt.Errorf("orchestrate: unsupported model %q: %w", m, err)
	}
	return resolved, nil
}

// subagentRunner returns the SubagentRunner wired into the server's
// toolSetConfig, reusing the same instance that NewServer installed.
func (s *Server) subagentRunner() claudetool.SubagentRunner {
	if r := s.toolSetConfig.SubagentRunner; r != nil {
		return r
	}
	return NewSubagentRunner(s)
}

// subagentDB returns the SubagentDB wired into the server's toolSetConfig.
func (s *Server) subagentDB() claudetool.SubagentDB {
	return s.toolSetConfig.SubagentDB
}

// plannerPrompt builds the prompt sent to the planner stage.
func plannerPrompt(goal string) string {
	return fmt.Sprintf("You are the PLANNER stage of an orchestrated build pipeline.\n\n"+
		"User goal:\n%s\n\n"+
		"Follow the \"planner\" skill. Produce a STRICT JSON plan that matches EXACTLY this schema "+
		"(no markdown commentary outside the JSON, no extra fields):\n\n"+
		"{\n"+
		"  \"goal\": \"<the user goal>\",\n"+
		"  \"batches\": [\n"+
		"    {\n"+
		"      \"id\": \"<batch id>\",\n"+
		"      \"mode\": \"parallel\" | \"sequential\",\n"+
		"      \"tasks\": [\n"+
		"        { \"id\": \"<task id>\", \"description\": \"<what to do>\", \"files\": [\"path/to/file.go\", ...] }\n"+
		"      ]\n"+
		"    }\n"+
		"  ]\n"+
		"}\n\n"+
		"Requirements:\n"+
		"- Every batch must have mode exactly \"parallel\" or \"sequential\".\n"+
		"- Every task must list the concrete files it will touch.\n"+
		"- Output ONLY the JSON, wrapped in a single ```json fenced block.", goal)
}

// builderPrompt builds the prompt sent to a single builder task.
func builderPrompt(task Task) string {
	files := strings.Join(task.Files, "\n")
	if files == "" {
		files = "(no files listed)"
	}
	return fmt.Sprintf(`You are a BUILDER stage of an orchestrated build pipeline.

Task ID: %s
Task description:
%s

Files you are permitted to touch (ONLY these):
%s

Follow the "builder" skill. Implement the task precisely. If you determine you need to modify a file NOT in the list above, STOP and report that in your final message instead of touching it. When done, give a short summary of what you changed.`, task.ID, task.Description, files)
}

// verifierPrompt builds the prompt sent to the verifier stage.
func verifierPrompt(goal string, plan Plan, batches []batchResult) string {
	var b strings.Builder
	b.WriteString("Plan (JSON):\n")
	planJSON, _ := json.Marshal(plan)
	b.Write(planJSON)
	b.WriteString("\n\nBuilder results per batch:\n")
	for _, br := range batches {
		fmt.Fprintf(&b, "Batch %s (%s):\n", br.ID, br.Mode)
		for _, t := range br.Tasks {
			fmt.Fprintf(&b, "  Task %s [%s]: %s\n", t.ID, t.Status, truncateForVerifier(t.Result))
		}
	}
	return "You are the VERIFIER stage of an orchestrated build pipeline.\n\n" +
		"User goal:\n" + goal + "\n\n" +
		b.String() + "\n\n" +
		"Follow the \"verifier\" skill. Run `go build`, `go test`, and `go vet` as appropriate. " +
		"Check that the builder results actually fulfill the goal and plan.\n\n" +
		"Respond with a verdict on the FIRST LINE, one of:\n" +
		"- PASS\n" +
		"- FAIL\n" +
		"- PASS WITH NOTES\n\n" +
		"Then on subsequent lines give details with file:line citations for any issues found."
}

func truncateForVerifier(s string) string {
	const max = 4000
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// extractVerifierVerdict returns the first non-empty line of the verifier
// output, trimmed, as the verdict.
func extractVerifierVerdict(text string) string {
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l != "" {
			return l
		}
	}
	return ""
}

// runOrchestration executes the planner -> parallel builders -> verifier
// pipeline, dispatching each stage to its own subagent conversation (and
// model) via the SubagentRunner primitive.
func (s *Server) runOrchestration(ctx context.Context, parentConversationID, cwd, goal string, opts orchestrateOptions) (orchestrateResult, error) {
	result := orchestrateResult{Goal: goal}

	// Resolve per-stage models (request override > const default), rejecting
	// unsupported models up front.
	plannerModel, err := s.resolveOrchestrateModel(opts.PlannerModel, defaultPlannerModel)
	if err != nil {
		return result, err
	}
	builderModel, err := s.resolveOrchestrateModel(opts.BuilderModel, defaultBuilderModel)
	if err != nil {
		return result, err
	}
	verifierModel, err := s.resolveOrchestrateModel(opts.VerifierModel, defaultVerifierModel)
	if err != nil {
		return result, err
	}

	runner := s.subagentRunner()
	dbAdapter := s.subagentDB()
	if dbAdapter == nil {
		return result, fmt.Errorf("orchestrate: subagent DB not configured")
	}

	plannerTimeout := 10 * time.Minute
	verifierTimeout := 10 * time.Minute

	// ---- Phase A: planner ------------------------------------------------
	plannerID, _, err := dbAdapter.GetOrCreateSubagentConversation(ctx, "orchestrate-planner", parentConversationID, cwd)
	if err != nil {
		return result, fmt.Errorf("orchestrate: failed to create planner conversation: %w", err)
	}
	rawPlan, err := runner.RunSubagent(ctx, plannerID, plannerPrompt(goal), true, plannerTimeout, plannerModel)
	if err != nil {
		return result, fmt.Errorf("orchestrate: planner failed: %w", err)
	}
	result.RawPlan = rawPlan

	plan, err := parsePlan(rawPlan)
	if err != nil {
		return result, err
	}
	result.Plan = plan

	// ---- Phase B: builders ----------------------------------------------
	concurrency := clampConcurrency(opts.MaxConcurrency)
	for _, batch := range plan.Batches {
		select {
		case <-ctx.Done():
			result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: cancelled during batch %q", batch.ID))
			return result, ctx.Err()
		default:
		}

		br := batchResult{ID: batch.ID, Mode: batch.Mode}
		if batch.Mode == "parallel" {
			br.Tasks = runParallelBuilders(ctx, runner, dbAdapter, parentConversationID, cwd, builderModel, batch.Tasks, concurrency, &result)
		} else {
			br.Tasks = runSequentialBuilders(ctx, runner, dbAdapter, parentConversationID, cwd, builderModel, batch.Tasks, &result)
		}
		result.Batches = append(result.Batches, br)
	}

	// ---- Phase C: verifier ----------------------------------------------
	if opts.SkipVerify {
		result.Verifier = verifierResult{Skipped: true}
		return result, nil
	}

	verifierID, _, err := dbAdapter.GetOrCreateSubagentConversation(ctx, "orchestrate-verifier", parentConversationID, cwd)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: failed to create verifier conversation: %v", err))
		return result, nil
	}
	verifierOut, err := runner.RunSubagent(ctx, verifierID, verifierPrompt(goal, plan, result.Batches), true, verifierTimeout, verifierModel)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: verifier failed: %v", err))
		return result, nil
	}
	result.Verifier = verifierResult{
		Verdict: extractVerifierVerdict(verifierOut),
		Detail:  verifierOut,
	}
	return result, nil
}

// runParallelBuilders runs a batch's tasks concurrently bounded by a
// semaphore channel of the given size. Per-task errors are appended to
// result.Errors and do not abort the batch (unless ctx is cancelled).
func runParallelBuilders(
	ctx context.Context,
	runner claudetool.SubagentRunner,
	dbAdapter claudetool.SubagentDB,
	parentConversationID, cwd, builderModel string,
	tasks []Task,
	concurrency int,
	result *orchestrateResult,
) []taskResult {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]taskResult, len(tasks))

	for i, task := range tasks {
		select {
		case <-ctx.Done():
			// Record remaining as cancelled and stop launching.
			mu.Lock()
			for j := i; j < len(tasks); j++ {
				results[j] = taskResult{ID: tasks[j].ID, Status: "error", Result: "cancelled before start"}
				result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: task %q cancelled", tasks[j].ID))
			}
			mu.Unlock()
			wg.Wait()
			return results
		default:
		}

		wg.Add(1)
		go func(idx int, t Task) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				results[idx] = taskResult{ID: t.ID, Status: "error", Result: "cancelled"}
				result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: task %q cancelled", t.ID))
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			tr := runOneBuilder(ctx, runner, dbAdapter, parentConversationID, cwd, builderModel, t)
			mu.Lock()
			results[idx] = tr
			if tr.Status == "error" {
				result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: task %q: %s", t.ID, tr.Result))
			}
			mu.Unlock()
		}(i, task)
	}
	wg.Wait()
	return results
}

// runSequentialBuilders runs a batch's tasks one at a time.
func runSequentialBuilders(
	ctx context.Context,
	runner claudetool.SubagentRunner,
	dbAdapter claudetool.SubagentDB,
	parentConversationID, cwd, builderModel string,
	tasks []Task,
	result *orchestrateResult,
) []taskResult {
	results := make([]taskResult, 0, len(tasks))
	for _, task := range tasks {
		if ctx.Err() != nil {
			results = append(results, taskResult{ID: task.ID, Status: "error", Result: "cancelled"})
			result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: task %q cancelled", task.ID))
			continue
		}
		tr := runOneBuilder(ctx, runner, dbAdapter, parentConversationID, cwd, builderModel, task)
		results = append(results, tr)
		if tr.Status == "error" {
			result.Errors = append(result.Errors, fmt.Sprintf("orchestrate: task %q: %s", task.ID, tr.Result))
		}
	}
	return results
}

// runOneBuilder spawns (or reuses) the builder subagent conversation for a
// task and runs it to completion, returning a taskResult.
func runOneBuilder(
	ctx context.Context,
	runner claudetool.SubagentRunner,
	dbAdapter claudetool.SubagentDB,
	parentConversationID, cwd, builderModel string,
	task Task,
) taskResult {
	slug := fmt.Sprintf("orchestrate-build-%s", task.ID)
	convID, _, err := dbAdapter.GetOrCreateSubagentConversation(ctx, slug, parentConversationID, cwd)
	if err != nil {
		return taskResult{ID: task.ID, Status: "error", Result: fmt.Sprintf("failed to create builder conversation: %v", err)}
	}
	out, err := runner.RunSubagent(ctx, convID, builderPrompt(task), true, 15*time.Minute, builderModel)
	if err != nil {
		if ctx.Err() != nil {
			return taskResult{ID: task.ID, Status: "error", Result: fmt.Sprintf("cancelled: %v", ctx.Err())}
		}
		return taskResult{ID: task.ID, Status: "error", Result: err.Error()}
	}
	return taskResult{ID: task.ID, Status: "success", Result: out}
}

// clampConcurrency bounds the builder concurrency to [1,4] with a default of 4.
func clampConcurrency(n int) int {
	if n <= 0 {
		return 4
	}
	if n > 4 {
		return 4
	}
	return n
}

// orchestrateRequest is the JSON body for POST /api/conversation/{id}/orchestrate.
type orchestrateRequest struct {
	Goal           string `json:"goal"`
	MaxConcurrency int    `json:"maxConcurrency"`
	SkipVerify     bool   `json:"skipVerify"`
	PlannerModel   string `json:"plannerModel"`
	BuilderModel   string `json:"builderModel"`
	VerifierModel  string `json:"verifierModel"`
}

// handleOrchestrateConversation runs an orchestrated planner->builders->verifier
// pipeline against the given conversation, dispatching each stage to its own
// subagent conversation and model. The route registration is handled elsewhere.
func (s *Server) handleOrchestrateConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	var req orchestrateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}

	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	// Orchestration must run from a top-level conversation: subagents cannot
	// themselves spawn orchestration subagents because MaxSubagentDepth==1
	// (set in NewServer). Allowing it would create a depth-2 subagent that the
	// tool layer refuses to spawn, so reject it explicitly with a clear error.
	if conv.ParentConversationID != nil {
		http.Error(w, "orchestration must run from a top-level conversation (subagents cannot orchestrate)", http.StatusBadRequest)
		return
	}

	cwd := ""
	if conv.Cwd != nil {
		cwd = *conv.Cwd
	}

	opts := orchestrateOptions{
		PlannerModel:   req.PlannerModel,
		BuilderModel:   req.BuilderModel,
		VerifierModel:  req.VerifierModel,
		MaxConcurrency: req.MaxConcurrency,
		SkipVerify:     req.SkipVerify,
	}

	result, err := s.runOrchestration(ctx, conversationID, cwd, req.Goal, opts)
	if err != nil {
		s.logger.Error("orchestrate: run failed", "conversationID", conversationID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result) //nolint:errchkjson // best-effort HTTP response
}
