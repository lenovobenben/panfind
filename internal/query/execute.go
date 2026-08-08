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

type ResultVisitor func(Result) error

func Execute(snapshot *namespace.Snapshot, query Query) ([]Result, error) {
	return ExecuteAt(snapshot, query, time.Now())
}

func ExecuteAt(snapshot *namespace.Snapshot, query Query, now time.Time) ([]Result, error) {
	results := make([]Result, 0)
	err := ExecuteEachAt(snapshot, query, now, func(result Result) error {
		results = append(results, result)
		return nil
	})
	return results, err
}

// ExecuteEach streams matching nodes to visit without retaining a result set.
func ExecuteEach(snapshot *namespace.Snapshot, query Query, visit ResultVisitor) error {
	return ExecuteEachAt(snapshot, query, time.Now(), visit)
}

func ExecuteEachAt(snapshot *namespace.Snapshot, query Query, now time.Time, visit ResultVisitor) error {
	if visit == nil {
		return fmt.Errorf("result visitor is nil")
	}
	providerPrefix, startPath, err := splitRoot(query.Root)
	if err != nil {
		return err
	}
	start, exists := snapshot.Lookup(startPath)
	if !exists {
		return fmt.Errorf("query root does not exist: %s", query.Root)
	}
	startDepth := pathDepth(startPath)

	return snapshot.WalkEach(start, func(key namespace.NodeKey) error {
		node, exists := snapshot.Node(key)
		if !exists {
			return fmt.Errorf("snapshot node disappeared during query: %+v", key)
		}
		nodePath, err := snapshot.Path(key)
		if err != nil {
			return err
		}
		depth := pathDepth(nodePath) - startDepth
		if depth < query.MinDepth || query.MaxDepth != nil && depth > *query.MaxDepth {
			return nil
		}
		cloudPath := providerPrefix + nodePath
		if !matches(query.Expression, node, cloudPath, now) {
			return nil
		}
		return visit(Result{Path: nodePath, Node: node})
	})
}

func splitRoot(root string) (string, string, error) {
	separator := strings.IndexByte(root, ':')
	if separator <= 0 {
		return "", "", fmt.Errorf("invalid query root %q", root)
	}
	prefix := root[:separator+1]
	namespacePath := root[separator+1:]
	if namespacePath == "" {
		namespacePath = "/"
	}
	if !strings.HasPrefix(namespacePath, "/") || strings.ContainsRune(namespacePath, '\x00') {
		return "", "", fmt.Errorf("invalid query root %q", root)
	}
	return prefix, path.Clean(namespacePath), nil
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
