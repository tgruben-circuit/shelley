package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tgruben-circuit/percy/claudetool"
	"github.com/tgruben-circuit/percy/db"
	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/models"
	"github.com/tgruben-circuit/percy/skills"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// orchestrateFakeLLMManager is a minimal LLMProvider that also implements
// modelResolver. ResolveModelID returns the requested model unchanged so the
// orchestrator's resolveOrchestrateModel succeeds for both the stage defaults
// and any request-body overrides, without performing any real model lookup.
type orchestrateFakeLLMManager struct{}

func (m *orchestrateFakeLLMManager) GetService(string) (llm.Service, error) {
	return nil, nil
}

func (m *orchestrateFakeLLMManager) GetAvailableModels() []string { return nil }

func (m *orchestrateFakeLLMManager) HasModel(string) bool { return true }

func (m *orchestrateFakeLLMManager) GetModelInfo(string) *models.ModelInfo { return nil }

func (m *orchestrateFakeLLMManager) RefreshCustomModels() error { return nil }

func (m *orchestrateFakeLLMManager) ResolveModelID(modelID string) (string, error) {
	return modelID, nil
}

// fakeCall is a single recorded RunSubagent invocation.
type fakeCall struct {
	Order   int
	ConvID  string
	Prompt  string
	Wait    bool
	Timeout time.Duration
	Model   string
	Stage   string // "planner" | "builder" | "verifier"
	TaskID  string // populated for builder calls
}

// fakeSubagentRunner implements claudetool.SubagentRunner. It records every
// call (order + model + stage) under a mutex and returns scripted responses
// keyed off the prompt content. Builder calls can be gated on per-task
// release channels to enable deterministic concurrency assertions.
type fakeSubagentRunner struct {
	mu sync.Mutex

	calls []fakeCall

	planResponse     string
	verifierResponse string

	// builderGate lets tests hold builder tasks in-flight until released.
	builderGate *builderGate
}

// builderGate gives each builder task a release channel and tracks the
// in-flight / max-observed in-flight counts via atomics so tests can verify
// the concurrency cap without any time.Sleep.
type builderGate struct {
	mu       sync.Mutex
	releases map[string]chan struct{}

	inFlight    int32
	maxInFlight int32

	// entered is sent a taskID immediately after that task increments
	// inFlight (and before blocking on its release channel). Buffered so the
	// runner never blocks on a test that isn't reading yet.
	entered chan string
}

func newBuilderGate() *builderGate {
	return &builderGate{
		releases: make(map[string]chan struct{}),
		entered:  make(chan string, 16),
	}
}

func (g *builderGate) release(taskID string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.releases[taskID] == nil {
		g.releases[taskID] = make(chan struct{})
	}
	return g.releases[taskID]
}

func (g *builderGate) signal(taskID string) {
	for {
		cur := atomic.LoadInt32(&g.maxInFlight)
		now := atomic.LoadInt32(&g.inFlight)
		if now <= cur || atomic.CompareAndSwapInt32(&g.maxInFlight, cur, now) {
			break
		}
	}
	g.entered <- taskID
}

func (g *builderGate) free(taskID string) {
	g.mu.Lock()
	ch, ok := g.releases[taskID]
	g.mu.Unlock()
	if ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (g *builderGate) freeAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, ch := range g.releases {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (g *builderGate) maxObserved() int32 { return atomic.LoadInt32(&g.maxInFlight) }

// RunSubagent implements claudetool.SubagentRunner.
func (r *fakeSubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration, model string) (string, error) {
	stage, taskID := classifyPrompt(prompt)

	r.mu.Lock()
	r.calls = append(r.calls, fakeCall{
		Order:   len(r.calls),
		ConvID:  conversationID,
		Prompt:  prompt,
		Wait:    wait,
		Timeout: timeout,
		Model:   model,
		Stage:   stage,
		TaskID:  taskID,
	})
	r.mu.Unlock()

	switch stage {
	case "planner":
		if r.planResponse == "" {
			return "", fmt.Errorf("fakeSubagentRunner: no planResponse configured")
		}
		return r.planResponse, nil
	case "verifier":
		if r.verifierResponse == "" {
			return "PASS\nverifier ok", nil
		}
		return r.verifierResponse, nil
	case "builder":
		if r.builderGate != nil {
			atomic.AddInt32(&r.builderGate.inFlight, 1)
			r.builderGate.signal(taskID)
			select {
			case <-r.builderGate.release(taskID):
			case <-ctx.Done():
				atomic.AddInt32(&r.builderGate.inFlight, -1)
				return "", ctx.Err()
			}
			atomic.AddInt32(&r.builderGate.inFlight, -1)
		}
		return fmt.Sprintf("builder ack for task %s", taskID), nil
	default:
		return "", fmt.Errorf("fakeSubagentRunner: unknown stage for prompt: %q", prompt)
	}
}

// classifyPrompt determines which orchestration stage a prompt belongs to and,
// for builder prompts, extracts the Task ID. The prompts are produced by
// plannerPrompt / builderPrompt / verifierPrompt in orchestrate.go.
func classifyPrompt(prompt string) (stage, taskID string) {
	switch {
	case strings.Contains(prompt, "PLANNER stage"):
		return "planner", ""
	case strings.Contains(prompt, "VERIFIER stage"):
		return "verifier", ""
	case strings.Contains(prompt, "BUILDER stage"):
		return "builder", parseBuilderTaskID(prompt)
	default:
		return "unknown", ""
	}
}

// parseBuilderTaskID extracts the "Task ID: <id>" field from a builder prompt.
func parseBuilderTaskID(prompt string) string {
	idx := strings.Index(prompt, "Task ID:")
	if idx < 0 {
		return ""
	}
	rest := prompt[idx+len("Task ID:"):]
	end := strings.IndexByte(rest, '\n')
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// callsOf returns a copy of the recorded calls filtered by stage.
func (r *fakeSubagentRunner) callsOf(stage string) []fakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []fakeCall
	for _, c := range r.calls {
		if c.Stage == stage {
			out = append(out, c)
		}
	}
	return out
}

func (r *fakeSubagentRunner) allCalls() []fakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]fakeCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *fakeSubagentRunner) verifierInvoked() bool {
	return len(r.callsOf("verifier")) > 0
}

// ---------------------------------------------------------------------------
// Test server helpers
// ---------------------------------------------------------------------------

// newOrchestrateTestServer builds a Server wired to a temp DB and the given
// fake runner. NewServer installs its own SubagentRunner; we overwrite that
// field with the fake so runOrchestration dispatches to it via
// s.subagentRunner() (which prefers toolSetConfig.SubagentRunner).
func newOrchestrateTestServer(t *testing.T, fake *fakeSubagentRunner) (*Server, *db.DB, func()) {
	t.Helper()
	database, dbCleanup := setupTestDB(t)
	logger := discardLogger()
	cfg := claudetool.ToolSetConfig{EnableBrowser: false}
	s := NewServer(database, &orchestrateFakeLLMManager{}, cfg, logger, false, "", "", "", nil)
	if fake != nil {
		s.toolSetConfig.SubagentRunner = fake
	}
	cleanup := func() {
		dbCleanup()
	}
	return s, database, cleanup
}

// newTopLevelConversation creates a top-level (non-subagent) conversation in
// the DB so the orchestrate handler accepts it.
func newTopLevelConversation(t *testing.T, database *db.DB) string {
	t.Helper()
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return conv.ConversationID
}

// validPlanJSON is a canned plan the planner fake returns. Two batches: one
// sequential (ordered) and one parallel.
const validPlanJSON = `{
  "goal": "test goal",
  "batches": [
    {"id": "seq", "mode": "sequential", "tasks": [
      {"id": "s1", "description": "do s1", "files": ["a.go"]},
      {"id": "s2", "description": "do s2", "files": ["b.go"]},
      {"id": "s3", "description": "do s3", "files": ["c.go"]}
    ]},
    {"id": "par", "mode": "parallel", "tasks": [
      {"id": "p1", "description": "do p1", "files": ["d.go"]},
      {"id": "p2", "description": "do p2", "files": ["e.go"]},
      {"id": "p3", "description": "do p3", "files": ["f.go"]}
    ]}
  ]
}`

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---------------------------------------------------------------------------
// (1) parsePlan unit tests
// ---------------------------------------------------------------------------

func TestParsePlan_ValidRawJSON(t *testing.T) {
	plan, err := parsePlan(validPlanJSON)
	if err != nil {
		t.Fatalf("parsePlan: unexpected error: %v", err)
	}
	if plan.Goal != "test goal" {
		t.Errorf("goal = %q, want %q", plan.Goal, "test goal")
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(plan.Batches))
	}
	if plan.Batches[0].Mode != "sequential" || plan.Batches[1].Mode != "parallel" {
		t.Errorf("batch modes = %q/%q", plan.Batches[0].Mode, plan.Batches[1].Mode)
	}
	if len(plan.Batches[0].Tasks) != 3 {
		t.Errorf("seq batch tasks = %d, want 3", len(plan.Batches[0].Tasks))
	}
}

func TestParsePlan_FencedJSONBlock(t *testing.T) {
	raw := "Here is the plan:\n\n```json\n" + validPlanJSON + "\n```\n\nLet me know."
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatalf("parsePlan: unexpected error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(plan.Batches))
	}
	if plan.Goal != "test goal" {
		t.Errorf("goal = %q, want %q", plan.Goal, "test goal")
	}
}

func TestParsePlan_MalformedJSON(t *testing.T) {
	raw := "```json\n{ \"goal\": \"x\", \"batches\": [ not valid json ] }\n```"
	if _, err := parsePlan(raw); err == nil {
		t.Fatal("parsePlan: expected error for malformed JSON, got nil")
	}
}

func TestParsePlan_NoBatches(t *testing.T) {
	raw := `{"goal": "x", "batches": []}`
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatal("parsePlan: expected error for empty batches, got nil")
	}
	if !strings.Contains(err.Error(), "no batches") {
		t.Errorf("error = %q, want it to mention 'no batches'", err.Error())
	}
}

func TestParsePlan_InvalidMode(t *testing.T) {
	raw := `{"goal": "x", "batches": [{"id": "b1", "mode": "wat", "tasks": [{"id":"t1","description":"d","files":["a.go"]}]}]}`
	if _, err := parsePlan(raw); err == nil {
		t.Fatal("parsePlan: expected error for invalid mode, got nil")
	}
}

// ---------------------------------------------------------------------------
// (2) Parallel-vs-sequential ordering
// ---------------------------------------------------------------------------

func TestRunOrchestration_SequentialBatchPreservesOrder(t *testing.T) {
	// A plan with a single sequential batch: tasks MUST be recorded in order.
	const seqOnlyPlan = `{
  "goal": "seq test",
  "batches": [
    {"id": "seq", "mode": "sequential", "tasks": [
      {"id": "s1", "description": "do s1", "files": ["a.go"]},
      {"id": "s2", "description": "do s2", "files": ["b.go"]},
      {"id": "s3", "description": "do s3", "files": ["c.go"]}
    ]}
  ]
}`
	fake := &fakeSubagentRunner{planResponse: seqOnlyPlan}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)
	ctx := context.Background()
	res, err := s.runOrchestration(ctx, parentID, "/cwd", "seq test", orchestrateOptions{SkipVerify: true})
	if err != nil {
		t.Fatalf("runOrchestration: %v", err)
	}

	// Sequential batch must record builder calls in task order s1, s2, s3.
	builders := fake.callsOf("builder")
	if len(builders) != 3 {
		t.Fatalf("builder calls = %d, want 3", len(builders))
	}
	want := []string{"s1", "s2", "s3"}
	for i, id := range want {
		if builders[i].TaskID != id {
			t.Errorf("builder call %d task = %q, want %q", i, builders[i].TaskID, id)
		}
		if builders[i].Order != i+1 { // +1 because planner is order 0
			t.Errorf("builder call %d order = %d, want %d", i, builders[i].Order, i+1)
		}
	}

	// The sequential batch result preserves task order.
	if len(res.Batches) != 1 {
		t.Fatalf("result batches = %d, want 1", len(res.Batches))
	}
	seq := res.Batches[0]
	if seq.Mode != "sequential" {
		t.Errorf("batch 0 mode = %q", seq.Mode)
	}
	for i, id := range want {
		if seq.Tasks[i].ID != id {
			t.Errorf("seq task %d = %q, want %q", i, seq.Tasks[i].ID, id)
		}
		if seq.Tasks[i].Status != "success" {
			t.Errorf("seq task %d status = %q", i, seq.Tasks[i].Status)
		}
	}
}

func TestRunOrchestration_ParallelBatchRunsAllTasks(t *testing.T) {
	fake := &fakeSubagentRunner{planResponse: validPlanJSON}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)
	ctx := context.Background()
	res, err := s.runOrchestration(ctx, parentID, "/cwd", "test goal", orchestrateOptions{SkipVerify: true})
	if err != nil {
		t.Fatalf("runOrchestration: %v", err)
	}

	// All 3 sequential + 3 parallel builder calls ran.
	builders := fake.callsOf("builder")
	if len(builders) != 6 {
		t.Fatalf("builder calls = %d, want 6", len(builders))
	}

	got := map[string]bool{}
	for _, c := range builders {
		got[c.TaskID] = true
	}
	for _, id := range []string{"s1", "s2", "s3", "p1", "p2", "p3"} {
		if !got[id] {
			t.Errorf("missing builder call for task %q", id)
		}
	}

	// Parallel batch result contains all 3 tasks (in stable index order).
	par := res.Batches[1]
	if par.Mode != "parallel" {
		t.Errorf("batch 1 mode = %q", par.Mode)
	}
	if len(par.Tasks) != 3 {
		t.Fatalf("parallel tasks = %d, want 3", len(par.Tasks))
	}
	for _, tk := range par.Tasks {
		if tk.Status != "success" {
			t.Errorf("parallel task %q status = %q", tk.ID, tk.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// (3) skipVerify=true
// ---------------------------------------------------------------------------

func TestRunOrchestration_SkipVerifySkipsVerifier(t *testing.T) {
	fake := &fakeSubagentRunner{planResponse: validPlanJSON}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)
	res, err := s.runOrchestration(context.Background(), parentID, "/cwd", "test goal", orchestrateOptions{SkipVerify: true})
	if err != nil {
		t.Fatalf("runOrchestration: %v", err)
	}

	if !res.Verifier.Skipped {
		t.Errorf("Verifier.Skipped = false, want true")
	}
	if res.Verifier.Verdict != "" {
		t.Errorf("Verifier.Verdict = %q, want empty when skipped", res.Verifier.Verdict)
	}
	if fake.verifierInvoked() {
		t.Errorf("verifier stage was invoked despite skipVerify=true; calls=%v", fake.callsOf("verifier"))
	}
}

func TestRunOrchestration_NoSkipRunsVerifier(t *testing.T) {
	fake := &fakeSubagentRunner{
		planResponse:     validPlanJSON,
		verifierResponse: "PASS\nall good",
	}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)
	res, err := s.runOrchestration(context.Background(), parentID, "/cwd", "test goal", orchestrateOptions{})
	if err != nil {
		t.Fatalf("runOrchestration: %v", err)
	}

	if res.Verifier.Skipped {
		t.Errorf("Verifier.Skipped = true, want false")
	}
	if res.Verifier.Verdict != "PASS" {
		t.Errorf("Verifier.Verdict = %q, want %q", res.Verifier.Verdict, "PASS")
	}
	if !fake.verifierInvoked() {
		t.Errorf("verifier stage was not invoked despite skipVerify=false")
	}
}

// ---------------------------------------------------------------------------
// (4) maxConcurrency limiting (channel-gated, no sleeps)
// ---------------------------------------------------------------------------

func TestRunOrchestration_MaxConcurrencyCapsInFlightBuilders(t *testing.T) {
	// A plan with a single parallel batch of 3 tasks. MaxConcurrency=2 must
	// never allow more than 2 builder RunSubagent calls to be in-flight at
	// once. We gate each builder task on a release channel so we can hold
	// tasks concurrently and observe the cap deterministically.
	const parallelOnlyPlan = `{
  "goal": "cap test",
  "batches": [
    {"id": "par", "mode": "parallel", "tasks": [
      {"id": "t1", "description": "d1", "files": ["a.go"]},
      {"id": "t2", "description": "d2", "files": ["b.go"]},
      {"id": "t3", "description": "d3", "files": ["c.go"]}
    ]}
  ]
}`

	gate := newBuilderGate()
	fake := &fakeSubagentRunner{planResponse: parallelOnlyPlan, builderGate: gate}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)

	type result struct {
		res orchestrateResult
		err error
	}
	done := make(chan result, 1)
	ctx := context.Background()
	go func() {
		res, err := s.runOrchestration(ctx, parentID, "/cwd", "cap test", orchestrateOptions{
			MaxConcurrency: 2,
			SkipVerify:     true,
		})
		done <- result{res, err}
	}()

	// Two tasks must enter immediately (semaphore size 2).
	id1 := mustRecv(t, gate.entered)
	id2 := mustRecv(t, gate.entered)

	// A third task must NOT have entered: the semaphore is full and both
	// in-flight tasks are blocked on their release channels, so no third
	// RunSubagent call can start. This non-blocking select is therefore
	// deterministic (not racy).
	select {
	case <-gate.entered:
		t.Fatal("third builder entered before a slot was freed; concurrency cap not enforced")
	default:
	}

	// Free one slot; exactly one more task should then enter.
	gate.free(id1)
	id3 := mustRecv(t, gate.entered)

	// Free the remaining two so the orchestration can finish.
	gate.free(id2)
	gate.free(id3)

	var res orchestrateResult
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("runOrchestration: %v", r.err)
		}
		res = r.res
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for runOrchestration to complete")
	}

	if len(res.Batches) != 1 {
		t.Fatalf("result batches = %d, want 1", len(res.Batches))
	}
	if len(res.Batches[0].Tasks) != 3 {
		t.Fatalf("parallel batch tasks = %d, want 3", len(res.Batches[0].Tasks))
	}

	// The high-water mark of concurrently in-flight builders must be exactly 2.
	if max := gate.maxObserved(); max != 2 {
		t.Errorf("max in-flight builders = %d, want 2", max)
	}

	// All three tasks ran.
	builders := fake.callsOf("builder")
	if len(builders) != 3 {
		t.Fatalf("builder calls = %d, want 3", len(builders))
	}
	got := map[string]bool{}
	for _, c := range builders {
		got[c.TaskID] = true
	}
	for _, id := range []string{id1, id2, id3} {
		if !got[id] {
			t.Errorf("missing builder call for task %q", id)
		}
	}
}

// ---------------------------------------------------------------------------
// (5) Per-stage model dispatch
// ---------------------------------------------------------------------------

func TestRunOrchestration_PerStageModelDefaults(t *testing.T) {
	fake := &fakeSubagentRunner{
		planResponse:     validPlanJSON,
		verifierResponse: "PASS\nok",
	}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)
	_, err := s.runOrchestration(context.Background(), parentID, "/cwd", "test goal", orchestrateOptions{})
	if err != nil {
		t.Fatalf("runOrchestration: %v", err)
	}

	planners := fake.callsOf("planner")
	if len(planners) != 1 {
		t.Fatalf("planner calls = %d, want 1", len(planners))
	}
	if planners[0].Model != defaultPlannerModel {
		t.Errorf("planner model = %q, want default %q", planners[0].Model, defaultPlannerModel)
	}

	for _, c := range fake.callsOf("builder") {
		if c.Model != defaultBuilderModel {
			t.Errorf("builder %q model = %q, want default %q", c.TaskID, c.Model, defaultBuilderModel)
		}
	}

	verifiers := fake.callsOf("verifier")
	if len(verifiers) != 1 {
		t.Fatalf("verifier calls = %d, want 1", len(verifiers))
	}
	if verifiers[0].Model != defaultVerifierModel {
		t.Errorf("verifier model = %q, want default %q", verifiers[0].Model, defaultVerifierModel)
	}
}

func TestRunOrchestration_PerStageModelOverrides(t *testing.T) {
	fake := &fakeSubagentRunner{
		planResponse:     validPlanJSON,
		verifierResponse: "PASS\nok",
	}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	parentID := newTopLevelConversation(t, database)
	opts := orchestrateOptions{
		PlannerModel:  "plan-override",
		BuilderModel:  "build-override",
		VerifierModel: "verify-override",
	}
	_, err := s.runOrchestration(context.Background(), parentID, "/cwd", "test goal", opts)
	if err != nil {
		t.Fatalf("runOrchestration: %v", err)
	}

	if got := fake.callsOf("planner")[0].Model; got != "plan-override" {
		t.Errorf("planner model = %q, want override %q", got, "plan-override")
	}
	for _, c := range fake.callsOf("builder") {
		if c.Model != "build-override" {
			t.Errorf("builder %q model = %q, want override %q", c.TaskID, c.Model, "build-override")
		}
	}
	if got := fake.callsOf("verifier")[0].Model; got != "verify-override" {
		t.Errorf("verifier model = %q, want override %q", got, "verify-override")
	}
}

// ---------------------------------------------------------------------------
// (6) HTTP handler tests
// ---------------------------------------------------------------------------

func TestHandleOrchestrateConversation_ValidGoal(t *testing.T) {
	fake := &fakeSubagentRunner{
		planResponse:     validPlanJSON,
		verifierResponse: "PASS\nok",
	}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	convID := newTopLevelConversation(t, database)

	body := `{"goal":"build the thing","maxConcurrency":2,"skipVerify":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+convID+"/orchestrate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleOrchestrateConversation(w, req, convID)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var res orchestrateResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response body into orchestrateResult: %v\nbody=%s", err, w.Body.String())
	}
	if res.Goal != "build the thing" {
		t.Errorf("result.Goal = %q, want %q", res.Goal, "build the thing")
	}
	if len(res.Plan.Batches) != 2 {
		t.Errorf("result.Plan.Batches = %d, want 2", len(res.Plan.Batches))
	}
	if res.Verifier.Skipped {
		t.Errorf("result.Verifier.Skipped = true, want false")
	}
	if res.Verifier.Verdict != "PASS" {
		t.Errorf("result.Verifier.Verdict = %q, want %q", res.Verifier.Verdict, "PASS")
	}
}

func TestHandleOrchestrateConversation_EmptyGoal(t *testing.T) {
	fake := &fakeSubagentRunner{planResponse: validPlanJSON}
	s, database, cleanup := newOrchestrateTestServer(t, fake)
	defer cleanup()

	convID := newTopLevelConversation(t, database)

	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+convID+"/orchestrate", strings.NewReader(`{"goal":""}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleOrchestrateConversation(w, req, convID)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if fake.verifierInvoked() {
		t.Errorf("verifier should not be invoked for an empty-goal rejection")
	}
}

// ---------------------------------------------------------------------------
// (7) Planner skill schema presence
// ---------------------------------------------------------------------------

func TestPlannerSkillSchemaPresence(t *testing.T) {
	skillPath := findRepoFile(t, filepath.Join("bundled_skills", "planner", "SKILL.md"))

	skill, err := skills.Parse(skillPath)
	if err != nil {
		t.Fatalf("skills.Parse(%q): %v", skillPath, err)
	}
	if skill.Name != "planner" {
		t.Errorf("skill.Name = %q, want %q", skill.Name, "planner")
	}
	if skill.Path == "" {
		t.Fatal("skill.Path is empty")
	}

	body, err := os.ReadFile(skill.Path)
	if err != nil {
		t.Fatalf("read skill body: %v", err)
	}
	bodyLower := strings.ToLower(string(body))

	for _, token := range []string{"batches", "parallel", "sequential", "mode"} {
		if !strings.Contains(bodyLower, token) {
			t.Errorf("planner SKILL.md body missing schema token %q", token)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// mustRecv receives a value from ch or fails the test (deterministic; no
// sleeps).
func mustRecv(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting on channel")
		return ""
	}
}

// findRepoFile walks up from the current working directory until it finds a
// directory containing go.mod, then joins rel onto it. go test runs with the
// package source directory as CWD (server/), so repo-relative paths need this
// resolution.
func findRepoFile(t *testing.T, rel string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod walking up from %s", cwd)
		}
		dir = parent
	}
}
