package llm

// modelPricing holds per-million-token pricing for known models.
type modelPricing struct {
	InputPerMillion      float64 // USD per 1M input tokens
	OutputPerMillion     float64 // USD per 1M output tokens
	CacheReadPerMillion  float64 // USD per 1M cache-read tokens (0 = same as input)
	CacheWritePerMillion float64 // USD per 1M cache-write tokens (0 = same as input)
}

// knownPricing maps API model names (as they appear in usage_data) to pricing.
// Prices are from public provider pricing pages.
var knownPricing = map[string]modelPricing{
	// Anthropic Claude
	"claude-fable-5":             {InputPerMillion: 10, OutputPerMillion: 50, CacheReadPerMillion: 1, CacheWritePerMillion: 12.5},
	"claude-opus-4-8":            {InputPerMillion: 5, OutputPerMillion: 25, CacheReadPerMillion: 0.5, CacheWritePerMillion: 6.25},
	"claude-opus-4-7":            {InputPerMillion: 5, OutputPerMillion: 25, CacheReadPerMillion: 0.5, CacheWritePerMillion: 6.25},
	"claude-sonnet-4-6":          {InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: 0.30, CacheWritePerMillion: 3.75},
	"claude-opus-4-6":            {InputPerMillion: 5, OutputPerMillion: 25, CacheReadPerMillion: 0.5, CacheWritePerMillion: 6.25},
	"claude-opus-4-5-20251101":   {InputPerMillion: 5, OutputPerMillion: 25, CacheReadPerMillion: 0.5, CacheWritePerMillion: 6.25},
	"claude-sonnet-4-5-20250929": {InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: 0.30, CacheWritePerMillion: 3.75},
	"claude-sonnet-4-20250514":   {InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: 0.30, CacheWritePerMillion: 3.75},
	"claude-haiku-4-5-20251001":  {InputPerMillion: 1, OutputPerMillion: 5, CacheReadPerMillion: 0.10, CacheWritePerMillion: 1.25},

	// OpenAI GPT-5 series (Codex / Responses)
	"gpt-5.3-codex": {InputPerMillion: 2, OutputPerMillion: 8},
	"gpt-5.2-codex": {InputPerMillion: 2, OutputPerMillion: 8},
	"gpt-5.1-codex": {InputPerMillion: 2, OutputPerMillion: 8},
	"gpt-5.1":       {InputPerMillion: 2, OutputPerMillion: 8},
	"gpt-5.1-mini":  {InputPerMillion: 0.40, OutputPerMillion: 1.60},
	"gpt-5.1-nano":  {InputPerMillion: 0.10, OutputPerMillion: 0.40},

	// Gemini
	"gemini-3-pro-preview":   {InputPerMillion: 1.25, OutputPerMillion: 10},
	"gemini-3-flash-preview": {InputPerMillion: 0.15, OutputPerMillion: 0.60},

	// Fireworks-hosted models (pay-per-token pricing)
	"accounts/fireworks/models/glm-4-7":           {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/glm-4-0414-p6":     {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/glm-5p2":           {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/glm-5p1":           {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/deepseek-v4-pro":   {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/deepseek-v4-flash": {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/kimi-k2p7-code":    {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/kimi-k2p6":         {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/kimi-k2p5":         {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"accounts/fireworks/models/kimi-k2-instruct":  {InputPerMillion: 0.90, OutputPerMillion: 0.90},
}

// EstimateCostUSD computes estimated cost from token counts and model name.
// Returns 0 if the model is not in the pricing table.
func EstimateCostUSD(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens uint64) float64 {
	p, ok := knownPricing[model]
	if !ok {
		return 0
	}

	inputRate := p.InputPerMillion
	cacheReadRate := p.CacheReadPerMillion
	if cacheReadRate == 0 {
		cacheReadRate = inputRate
	}
	cacheWriteRate := p.CacheWritePerMillion
	if cacheWriteRate == 0 {
		cacheWriteRate = inputRate
	}

	cost := float64(inputTokens)*inputRate/1_000_000 +
		float64(outputTokens)*p.OutputPerMillion/1_000_000 +
		float64(cacheReadTokens)*cacheReadRate/1_000_000 +
		float64(cacheWriteTokens)*cacheWriteRate/1_000_000
	return cost
}
