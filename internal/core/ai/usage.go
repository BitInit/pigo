package ai

// StopReason describes the state or termination of a model response.
type StopReason string

const (
	StopReasonPending StopReason = "pending"
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

// Usage follows pi's token accounting: Input excludes CacheRead and CacheWrite.
// Providers must normalize their native counters to these categories.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	// Reasoning is a subset of Output; nil means the provider did not report it.
	Reasoning   *int64     `json:"reasoning,omitempty"`
	TotalTokens int64      `json:"totalTokens"`
	Cost        *UsageCost `json:"cost,omitempty"`
}

// UsageCost records estimated USD costs; nil Usage.Cost means unavailable.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}
