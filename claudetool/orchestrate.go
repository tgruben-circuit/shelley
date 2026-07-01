package claudetool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tgruben-circuit/percy/llm"
)

// OrchestrateOptions controls per-stage model selection, concurrency, and
// whether the verifier stage runs.
type OrchestrateOptions struct {
	PlannerModel   string
	BuilderModel   string
	VerifierModel  string
	MaxConcurrency int
	SkipVerify     bool
}

// OrchestrateRunner runs a planner->builders->verifier pipeline. It is
// implemented by the server package to avoid an import cycle. It returns a
// human-readable summary of the run.
type OrchestrateRunner interface {
	RunOrchestration(ctx context.Context, parentConversationID, cwd, goal string, opts OrchestrateOptions) (string, error)
}

// OrchestrateTool dispatches a goal through the planner->builders->verifier
// pipeline, each stage running in its own subagent conversation and model.
type OrchestrateTool struct {
	ParentConversationID string
	WorkingDir           *MutableWorkingDir
	Runner               OrchestrateRunner
}

const (
	orchestrateName        = "orchestrate"
	orchestrateDescription = `Run a structured planner -> builders -> verifier pipeline for a larger goal.

Use this for multi-step features or refactors that benefit from being decomposed:
- A planner model breaks the goal into batches of independent tasks.
- Builder subagents implement the tasks (parallel or sequential per batch).
- A verifier model checks the result against the goal.

Each stage runs in its own subagent conversation and can use a different model.
Prefer this over spawning subagents by hand when the work is large enough to
warrant explicit planning and verification. Returns a summary of the run.`
	orchestrateInputSchema = `
{
  "type": "object",
  "required": ["goal"],
  "properties": {
    "goal": {
      "type": "string",
      "description": "The overall goal to plan, build, and verify."
    },
    "planner_model": {
      "type": "string",
      "description": "Model ID for the planner stage (optional; defaults to a strong reasoning model)."
    },
    "builder_model": {
      "type": "string",
      "description": "Model ID for the builder stage (optional)."
    },
    "verifier_model": {
      "type": "string",
      "description": "Model ID for the verifier stage (optional)."
    },
    "max_concurrency": {
      "type": "integer",
      "description": "Max parallel builders per batch (1-4, default 4)."
    },
    "skip_verify": {
      "type": "boolean",
      "description": "Skip the verifier stage (default false)."
    }
  }
}
`
)

type orchestrateInput struct {
	Goal           string `json:"goal"`
	PlannerModel   string `json:"planner_model,omitempty"`
	BuilderModel   string `json:"builder_model,omitempty"`
	VerifierModel  string `json:"verifier_model,omitempty"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`
	SkipVerify     bool   `json:"skip_verify,omitempty"`
}

// Tool returns an llm.Tool for the orchestrate functionality.
func (o *OrchestrateTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        orchestrateName,
		Description: strings.TrimSpace(orchestrateDescription),
		InputSchema: llm.MustSchema(orchestrateInputSchema),
		Run:         o.Run,
	}
}

func (o *OrchestrateTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req orchestrateInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to parse orchestrate input: %w", err)
	}
	if strings.TrimSpace(req.Goal) == "" {
		return llm.ErrorfToolOut("goal is required")
	}

	opts := OrchestrateOptions{
		PlannerModel:   req.PlannerModel,
		BuilderModel:   req.BuilderModel,
		VerifierModel:  req.VerifierModel,
		MaxConcurrency: req.MaxConcurrency,
		SkipVerify:     req.SkipVerify,
	}

	summary, err := o.Runner.RunOrchestration(ctx, o.ParentConversationID, o.WorkingDir.Get(), req.Goal, opts)
	if err != nil {
		return llm.ErrorfToolOut("orchestrate error: %w", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(summary)}
}
