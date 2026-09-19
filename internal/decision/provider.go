package decision

import (
	"context"
	"errors"
)

var (
	ErrUnavailable   = errors.New("decision provider unavailable")
	ErrInvalidResult = errors.New("decision provider returned invalid result")
)

type Provider interface {
	Name() string
	Boolean(context.Context, BooleanRequest) (BooleanResult, error)
	Choice(context.Context, ChoiceRequest) (ChoiceResult, error)
	Score(context.Context, ScoreRequest) (ScoreResult, error)
	Rank(context.Context, RankRequest) (RankResult, error)
}
