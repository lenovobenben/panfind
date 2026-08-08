package query

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type Result struct {
	Path string
	Node namespace.Node
}

func Execute(snapshot *namespace.Snapshot, query Query) ([]Result, error) {
	return ExecuteAt(snapshot, query, time.Now())
}

func ExecuteAt(snapshot *namespace.Snapshot, query Query, now time.Time) ([]Result, error) {
	keys, err := snapshot.Walk(snapshot.Root())
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0)
	for _, key := range keys {
		node, exists := snapshot.Node(key)
		if !exists {
			return nil, fmt.Errorf("snapshot node disappeared during query: %+v", key)
		}
		nodePath, err := snapshot.Path(key)
		if err != nil {
			return nil, err
		}
		depth := pathDepth(nodePath)
		if depth < query.MinDepth || query.MaxDepth != nil && depth > *query.MaxDepth {
			continue
		}
		cloudPath := strings.TrimSuffix(query.Root, "/") + nodePath
		if !matches(query.Expression, node, cloudPath, now) {
			continue
		}
		results = append(results, Result{Path: nodePath, Node: node})
	}
	return results, nil
}

func matches(expression Expression, node namespace.Node, cloudPath string, now time.Time) bool {
	switch expression := expression.(type) {
	case True:
		return true
	case And:
		return matches(expression.Left, node, cloudPath, now) && matches(expression.Right, node, cloudPath, now)
	case Or:
		return matches(expression.Left, node, cloudPath, now) || matches(expression.Right, node, cloudPath, now)
	case Not:
		return !matches(expression.Expression, node, cloudPath, now)
	case TypePredicate:
		return node.Kind == expression.Kind
	case NamePredicate:
		pattern := expression.Pattern
		name := node.Name
		if expression.CaseInsensitive {
			pattern = strings.ToLower(pattern)
			name = strings.ToLower(name)
		}
		matched, _ := path.Match(pattern, name)
		return matched
	case PathPredicate:
		pattern := expression.Pattern
		candidate := cloudPath
		if expression.CaseInsensitive {
			pattern = strings.ToLower(pattern)
			candidate = strings.ToLower(candidate)
		}
		matched, _ := path.Match(
			strings.ReplaceAll(pattern, "/", "\x00"),
			strings.ReplaceAll(candidate, "/", "\x00"),
		)
		return matched
	case SizePredicate:
		units := node.Size / expression.Unit
		if node.Size%expression.Unit != 0 {
			units++
		}
		switch expression.Comparison {
		case Greater:
			return units > expression.Count
		case Less:
			return units < expression.Count
		default:
			return units == expression.Count
		}
	case MTimePredicate:
		if node.ModifiedAt == nil {
			return false
		}
		ageDays := int64(math.Floor(now.Sub(*node.ModifiedAt).Hours() / 24))
		switch expression.Comparison {
		case Greater:
			return ageDays > expression.Days
		case Less:
			return ageDays < expression.Days
		default:
			return ageDays == expression.Days
		}
	case NewerMTPredicate:
		return node.ModifiedAt != nil && node.ModifiedAt.After(expression.Reference)
	default:
		return false
	}
}

func pathDepth(value string) int {
	trimmed := strings.Trim(value, "/")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "/") + 1
}
