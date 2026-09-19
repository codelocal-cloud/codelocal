package decision

import (
	"context"
	"math"
	"strings"
	"time"
)

const defaultTimeout = 750 * time.Millisecond

type Config struct {
	Timeout       time.Duration
	MinConfidence float64
}

type Engine struct {
	primary  Provider
	fallback Provider
	config   Config
}

func New(primary, fallback Provider, config Config) *Engine {
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	if config.MinConfidence < 0 || config.MinConfidence > 1 {
		config.MinConfidence = 0
	}
	return &Engine{primary: primary, fallback: fallback, config: config}
}

func (e *Engine) Boolean(ctx context.Context, req BooleanRequest) (BooleanResult, error) {
	result, err := execute(ctx, e.primary, e.config.Timeout, func(ctx context.Context, provider Provider) (BooleanResult, error) {
		return provider.Boolean(ctx, req)
	})
	if err == nil && validMeta(result.Meta, e.config.MinConfidence) {
		return result, nil
	}
	fallback, fallbackErr := executeFallback(ctx, e.fallback, e.config.Timeout, func(ctx context.Context, provider Provider) (BooleanResult, error) {
		return provider.Boolean(ctx, req)
	})
	if fallbackErr != nil {
		return BooleanResult{}, fallbackErr
	}
	if !validMeta(fallback.Meta, 0) {
		return BooleanResult{}, ErrInvalidResult
	}
	return fallback, nil
}

func (e *Engine) Choice(ctx context.Context, req ChoiceRequest) (ChoiceResult, error) {
	result, err := execute(ctx, e.primary, e.config.Timeout, func(ctx context.Context, provider Provider) (ChoiceResult, error) {
		return provider.Choice(ctx, req)
	})
	if err == nil && validChoice(req, result, e.config.MinConfidence) {
		return result, nil
	}
	fallback, fallbackErr := executeFallback(ctx, e.fallback, e.config.Timeout, func(ctx context.Context, provider Provider) (ChoiceResult, error) {
		return provider.Choice(ctx, req)
	})
	if fallbackErr != nil {
		return ChoiceResult{}, fallbackErr
	}
	if !validChoice(req, fallback, 0) {
		return ChoiceResult{}, ErrInvalidResult
	}
	return fallback, nil
}

func (e *Engine) Score(ctx context.Context, req ScoreRequest) (ScoreResult, error) {
	result, err := execute(ctx, e.primary, e.config.Timeout, func(ctx context.Context, provider Provider) (ScoreResult, error) {
		return provider.Score(ctx, req)
	})
	if err == nil && validProbability(result.Score) && validMeta(result.Meta, e.config.MinConfidence) {
		return result, nil
	}
	fallback, fallbackErr := executeFallback(ctx, e.fallback, e.config.Timeout, func(ctx context.Context, provider Provider) (ScoreResult, error) {
		return provider.Score(ctx, req)
	})
	if fallbackErr != nil {
		return ScoreResult{}, fallbackErr
	}
	if !validProbability(fallback.Score) || !validMeta(fallback.Meta, 0) {
		return ScoreResult{}, ErrInvalidResult
	}
	return fallback, nil
}

func (e *Engine) Rank(ctx context.Context, req RankRequest) (RankResult, error) {
	result, err := execute(ctx, e.primary, e.config.Timeout, func(ctx context.Context, provider Provider) (RankResult, error) {
		return provider.Rank(ctx, req)
	})
	if err == nil && validRank(req, result, e.config.MinConfidence) {
		return result, nil
	}
	fallback, fallbackErr := executeFallback(ctx, e.fallback, e.config.Timeout, func(ctx context.Context, provider Provider) (RankResult, error) {
		return provider.Rank(ctx, req)
	})
	if fallbackErr != nil {
		return RankResult{}, fallbackErr
	}
	if !validRank(req, fallback, 0) {
		return RankResult{}, ErrInvalidResult
	}
	return fallback, nil
}

func execute[T any](ctx context.Context, provider Provider, timeout time.Duration, call func(context.Context, Provider) (T, error)) (T, error) {
	var zero T
	if provider == nil {
		return zero, ErrUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	result, err := call(callCtx, provider)
	if err != nil {
		return zero, err
	}
	setMetaLatency(any(&result), provider.Name(), time.Since(started), false)
	return result, nil
}

func executeFallback[T any](ctx context.Context, provider Provider, timeout time.Duration, call func(context.Context, Provider) (T, error)) (T, error) {
	var zero T
	if provider == nil {
		return zero, ErrUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	result, err := call(callCtx, provider)
	if err != nil {
		return zero, err
	}
	setMetaLatency(any(&result), provider.Name(), time.Since(started), true)
	return result, nil
}

func setMetaLatency(result any, provider string, latency time.Duration, fallback bool) {
	var meta *Meta
	switch value := result.(type) {
	case *BooleanResult:
		meta = &value.Meta
	case *ChoiceResult:
		meta = &value.Meta
	case *ScoreResult:
		meta = &value.Meta
	case *RankResult:
		meta = &value.Meta
	}
	if meta == nil {
		return
	}
	if strings.TrimSpace(meta.Provider) == "" {
		meta.Provider = strings.TrimSpace(provider)
	}
	meta.Latency = latency
	meta.LatencyMS = latency.Milliseconds()
	meta.Fallback = fallback
}

func validMeta(meta Meta, minConfidence float64) bool {
	return validProbability(meta.Confidence) && meta.Confidence >= minConfidence
}

func validChoice(req ChoiceRequest, result ChoiceResult, minConfidence float64) bool {
	if !validMeta(result.Meta, minConfidence) || strings.TrimSpace(result.ChoiceID) == "" {
		return false
	}
	for _, candidate := range req.Candidates {
		if candidate.ID == result.ChoiceID {
			return true
		}
	}
	return false
}

func validRank(req RankRequest, result RankResult, minConfidence float64) bool {
	if !validMeta(result.Meta, minConfidence) || len(result.Items) == 0 {
		return false
	}
	allowed := make(map[string]struct{}, len(req.Candidates))
	for _, candidate := range req.Candidates {
		allowed[candidate.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, item := range result.Items {
		if _, ok := allowed[item.ID]; !ok || !validProbability(item.Score) {
			return false
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return true
}

func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
