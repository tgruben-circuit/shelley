package llm

import (
	"math"
	"testing"
)

func TestEstimateCostUSD(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		input       uint64
		output      uint64
		cacheRead   uint64
		cacheWrite  uint64
		wantApprox  float64
	}{
		{
			name:   "claude opus 4.6 basic",
			model:  "claude-opus-4-6",
			input:  1000,
			output: 500,
			// 1000 * 5/1M + 500 * 25/1M = 0.005 + 0.0125 = 0.0175
			wantApprox: 0.0175,
		},
		{
			name:       "claude opus 4.6 with cache",
			model:      "claude-opus-4-6",
			input:      100,
			output:     200,
			cacheRead:  5000,
			cacheWrite: 3000,
			// 100*5/1M + 200*25/1M + 5000*0.5/1M + 3000*6.25/1M
			// = 0.0005 + 0.005 + 0.0025 + 0.01875 = 0.02675
			wantApprox: 0.02675,
		},
		{
			name:       "unknown model returns 0",
			model:      "unknown-model",
			input:      1000,
			output:     1000,
			wantApprox: 0,
		},
		{
			name:       "gpt-5.3-codex",
			model:      "gpt-5.3-codex",
			input:      10000,
			output:     2000,
			// 10000*2/1M + 2000*8/1M = 0.02 + 0.016 = 0.036
			wantApprox: 0.036,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCostUSD(tt.model, tt.input, tt.output, tt.cacheRead, tt.cacheWrite)
			if math.Abs(got-tt.wantApprox) > 0.0001 {
				t.Errorf("EstimateCostUSD() = %f, want ≈ %f", got, tt.wantApprox)
			}
		})
	}
}

func TestFillCostEstimate(t *testing.T) {
	// Already has cost — should not overwrite
	u := Usage{Model: "claude-opus-4-6", InputTokens: 1000, OutputTokens: 500, CostUSD: 0.99}
	u.FillCostEstimate()
	if u.CostUSD != 0.99 {
		t.Errorf("FillCostEstimate should not overwrite existing cost, got %f", u.CostUSD)
	}

	// Zero cost with model — should fill
	u2 := Usage{Model: "claude-opus-4-6", InputTokens: 1000, OutputTokens: 500}
	u2.FillCostEstimate()
	if u2.CostUSD == 0 {
		t.Error("FillCostEstimate should have filled cost")
	}

	// No model — should not fill
	u3 := Usage{InputTokens: 1000, OutputTokens: 500}
	u3.FillCostEstimate()
	if u3.CostUSD != 0 {
		t.Errorf("FillCostEstimate without model should remain 0, got %f", u3.CostUSD)
	}
}
