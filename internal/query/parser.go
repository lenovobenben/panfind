package query

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type parser struct {
	tokens []string
	pos    int
}

func Parse(root string, tokens []string) (Query, error) {
	query := Query{Root: root, Expression: True{}}
	p := parser{tokens: tokens}

	// Depth controls are global traversal options and must precede predicates.
	for !p.atEnd() && (p.peek() == "-mindepth" || p.peek() == "-maxdepth") {
		operator := p.advance()
		value, err := p.requireArgument(operator)
		if err != nil {
			return Query{}, err
		}
		depth, err := strconv.Atoi(value)
		if err != nil || depth < 0 {
			return Query{}, fmt.Errorf("%s requires a non-negative integer", operator)
		}
		if operator == "-mindepth" {
			query.MinDepth = depth
		} else {
			query.MaxDepth = pointer(depth)
		}
	}

	if p.atEnd() {
		return query, nil
	}
	expression, err := p.parseOr()
	if err != nil {
		return Query{}, err
	}
	if p.pos != len(p.tokens) {
		return Query{}, fmt.Errorf("unexpected query token %q", p.tokens[p.pos])
	}
	query.Expression = expression
	return query, nil
}

// parseOr implements the lowest-precedence operator.
func (p *parser) parseOr() (Expression, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for p.match("-o", "-or") {
		operator := p.tokens[p.pos-1]
		if p.atEnd() || p.peek() == ")" {
			return nil, fmt.Errorf("%s requires an expression on the right", operator)
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = Or{Left: left, Right: right}
	}
	return left, nil
}

// parseAnd handles explicit -a and the implicit AND between adjacent terms.
func (p *parser) parseAnd() (Expression, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}

	for !p.atEnd() && p.peek() != ")" && p.peek() != "-o" && p.peek() != "-or" {
		if p.match("-a", "-and") {
			operator := p.tokens[p.pos-1]
			if p.atEnd() || p.peek() == ")" || p.peek() == "-o" || p.peek() == "-or" {
				return nil, fmt.Errorf("%s requires an expression on the right", operator)
			}
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = And{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseUnary() (Expression, error) {
	if p.match("!", "-not") {
		operator := p.tokens[p.pos-1]
		if p.atEnd() || p.peek() == ")" {
			return nil, fmt.Errorf("%s requires an expression", operator)
		}
		expression, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return Not{Expression: expression}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expression, error) {
	if p.atEnd() {
		return nil, fmt.Errorf("expected query expression")
	}

	if p.match("(") {
		if p.atEnd() || p.peek() == ")" {
			return nil, fmt.Errorf("empty parenthesized expression")
		}
		expression, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.match(")") {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		return expression, nil
	}
	if p.peek() == ")" {
		return nil, fmt.Errorf("unexpected closing parenthesis")
	}

	operator := p.advance()
	switch operator {
	case "-type":
		value, err := p.requireArgument(operator)
		if err != nil {
			return nil, err
		}
		var kind namespace.NodeKind
		switch value {
		case "f":
			kind = namespace.NodeKindFile
		case "d":
			kind = namespace.NodeKindDirectory
		default:
			return nil, fmt.Errorf("unsupported -type value %q; expected f or d", value)
		}
		return TypePredicate{Kind: kind}, nil
	case "-name", "-iname":
		pattern, err := p.requireArgument(operator)
		if err != nil {
			return nil, err
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("invalid %s pattern %q: %w", operator, pattern, err)
		}
		return NamePredicate{Pattern: pattern, CaseInsensitive: operator == "-iname"}, nil
	case "-path", "-ipath":
		pattern, err := p.requireArgument(operator)
		if err != nil {
			return nil, err
		}
		if _, err := path.Match(strings.ReplaceAll(pattern, "/", "\x00"), ""); err != nil {
			return nil, fmt.Errorf("invalid %s pattern %q: %w", operator, pattern, err)
		}
		return PathPredicate{Pattern: pattern, CaseInsensitive: operator == "-ipath"}, nil
	case "-size":
		value, err := p.requireArgument(operator)
		if err != nil {
			return nil, err
		}
		return parseSize(value)
	case "-mtime":
		value, err := p.requireArgument(operator)
		if err != nil {
			return nil, err
		}
		return parseMTime(value)
	case "-newermt":
		value, err := p.requireArgument(operator)
		if err != nil {
			return nil, err
		}
		reference, err := parseReferenceTime(value, time.Local)
		if err != nil {
			return nil, err
		}
		return NewerMTPredicate{Reference: reference}, nil
	case "-mindepth", "-maxdepth":
		return nil, fmt.Errorf("%s must appear before query predicates", operator)
	default:
		return nil, fmt.Errorf("unsupported query token %q", operator)
	}
}

func (p *parser) requireArgument(operator string) (string, error) {
	if p.atEnd() {
		return "", fmt.Errorf("%s requires an argument", operator)
	}
	value := p.advance()
	if value == "(" || value == ")" || value == "-a" || value == "-and" || value == "-o" || value == "-or" {
		return "", fmt.Errorf("%s requires an argument", operator)
	}
	return value, nil
}

func (p *parser) atEnd() bool {
	return p.pos >= len(p.tokens)
}

func (p *parser) peek() string {
	return p.tokens[p.pos]
}

func (p *parser) advance() string {
	token := p.tokens[p.pos]
	p.pos++
	return token
}

func (p *parser) match(values ...string) bool {
	if p.atEnd() {
		return false
	}
	for _, value := range values {
		if p.peek() == value {
			p.pos++
			return true
		}
	}
	return false
}

func parseSize(value string) (SizePredicate, error) {
	if value == "" {
		return SizePredicate{}, fmt.Errorf("invalid empty -size value")
	}

	comparison := Equal
	switch value[0] {
	case '+':
		comparison = Greater
		value = value[1:]
	case '-':
		comparison = Less
		value = value[1:]
	}
	if value == "" {
		return SizePredicate{}, fmt.Errorf("invalid -size value")
	}

	unit := uint64(512)
	last := value[len(value)-1]
	if last < '0' || last > '9' {
		switch last {
		case 'c':
			unit = 1
		case 'w':
			unit = 2
		case 'b':
			unit = 512
		case 'k':
			unit = 1024
		case 'M':
			unit = 1024 * 1024
		case 'G':
			unit = 1024 * 1024 * 1024
		default:
			return SizePredicate{}, fmt.Errorf("unsupported -size unit %q", string(last))
		}
		value = value[:len(value)-1]
	}

	count, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return SizePredicate{}, fmt.Errorf("invalid -size value %q", value)
	}
	return SizePredicate{Comparison: comparison, Count: count, Unit: unit}, nil
}

func parseMTime(value string) (MTimePredicate, error) {
	comparison, number := splitComparison(value)
	if number == "" {
		return MTimePredicate{}, fmt.Errorf("invalid -mtime value %q", value)
	}
	days, err := strconv.ParseUint(number, 10, 64)
	if err != nil || days > math.MaxInt64 {
		return MTimePredicate{}, fmt.Errorf("invalid -mtime value %q", value)
	}
	return MTimePredicate{Comparison: comparison, Days: int64(days)}, nil
}

func splitComparison(value string) (Comparison, string) {
	if value == "" {
		return Equal, value
	}
	switch value[0] {
	case '+':
		return Greater, value[1:]
	case '-':
		return Less, value[1:]
	default:
		return Equal, value
	}
}

func parseReferenceTime(value string, location *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid -newermt value %q; use YYYY-MM-DD, local date-time, or RFC3339", value)
}

func pointer[T any](value T) *T {
	return &value
}
