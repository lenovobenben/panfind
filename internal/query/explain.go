package query

import (
	"fmt"
	"time"
)

type Explanation struct {
	Root       string                `json:"root"`
	MinDepth   int                   `json:"min_depth"`
	MaxDepth   *int                  `json:"max_depth,omitempty"`
	Expression ExpressionExplanation `json:"expression"`
}

type ExpressionExplanation struct {
	Operator        string                 `json:"operator"`
	Left            *ExpressionExplanation `json:"left,omitempty"`
	Right           *ExpressionExplanation `json:"right,omitempty"`
	Expression      *ExpressionExplanation `json:"expression,omitempty"`
	Kind            string                 `json:"kind,omitempty"`
	Pattern         string                 `json:"pattern,omitempty"`
	Comparison      string                 `json:"comparison,omitempty"`
	Count           *uint64                `json:"count,omitempty"`
	UnitBytes       *uint64                `json:"unit_bytes,omitempty"`
	Days            *int64                 `json:"days,omitempty"`
	Reference       string                 `json:"reference,omitempty"`
	CaseInsensitive bool                   `json:"case_insensitive,omitempty"`
}

func Explain(query Query) (Explanation, error) {
	expression, err := explainExpression(query.Expression)
	if err != nil {
		return Explanation{}, err
	}
	return Explanation{
		Root:       query.Root,
		MinDepth:   query.MinDepth,
		MaxDepth:   query.MaxDepth,
		Expression: expression,
	}, nil
}

func explainExpression(expression Expression) (ExpressionExplanation, error) {
	switch expression := expression.(type) {
	case True:
		return ExpressionExplanation{Operator: "true"}, nil
	case And:
		left, err := explainExpression(expression.Left)
		if err != nil {
			return ExpressionExplanation{}, err
		}
		right, err := explainExpression(expression.Right)
		if err != nil {
			return ExpressionExplanation{}, err
		}
		return ExpressionExplanation{Operator: "and", Left: &left, Right: &right}, nil
	case Or:
		left, err := explainExpression(expression.Left)
		if err != nil {
			return ExpressionExplanation{}, err
		}
		right, err := explainExpression(expression.Right)
		if err != nil {
			return ExpressionExplanation{}, err
		}
		return ExpressionExplanation{Operator: "or", Left: &left, Right: &right}, nil
	case Not:
		inner, err := explainExpression(expression.Expression)
		if err != nil {
			return ExpressionExplanation{}, err
		}
		return ExpressionExplanation{Operator: "not", Expression: &inner}, nil
	case TypePredicate:
		return ExpressionExplanation{Operator: "type", Kind: expression.Kind.String()}, nil
	case NamePredicate:
		return ExpressionExplanation{Operator: "name", Pattern: expression.Pattern, CaseInsensitive: expression.CaseInsensitive}, nil
	case PathPredicate:
		return ExpressionExplanation{Operator: "path", Pattern: expression.Pattern, CaseInsensitive: expression.CaseInsensitive}, nil
	case SizePredicate:
		count := expression.Count
		unit := expression.Unit
		return ExpressionExplanation{Operator: "size", Comparison: comparisonName(expression.Comparison), Count: &count, UnitBytes: &unit}, nil
	case MTimePredicate:
		days := expression.Days
		return ExpressionExplanation{Operator: "mtime", Comparison: comparisonName(expression.Comparison), Days: &days}, nil
	case NewerMTPredicate:
		return ExpressionExplanation{Operator: "newermt", Reference: expression.Reference.Format(time.RFC3339)}, nil
	default:
		return ExpressionExplanation{}, fmt.Errorf("cannot explain expression type %T", expression)
	}
}

func comparisonName(comparison Comparison) string {
	switch comparison {
	case Greater:
		return "greater"
	case Less:
		return "less"
	default:
		return "equal"
	}
}
