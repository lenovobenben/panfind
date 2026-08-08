// Package query parses and executes provider-neutral metadata queries.
package query

import (
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type Query struct {
	Root       string
	Expression Expression
	MinDepth   int
	MaxDepth   *int
}

type Expression interface {
	isExpression()
}

type True struct{}

func (True) isExpression() {}

type And struct {
	Left  Expression
	Right Expression
}

func (And) isExpression() {}

type Or struct {
	Left  Expression
	Right Expression
}

func (Or) isExpression() {}

type Not struct {
	Expression Expression
}

func (Not) isExpression() {}

type TypePredicate struct {
	Kind namespace.NodeKind
}

func (TypePredicate) isExpression() {}

type NamePredicate struct {
	Pattern         string
	CaseInsensitive bool
}

func (NamePredicate) isExpression() {}

type PathPredicate struct {
	Pattern         string
	CaseInsensitive bool
}

func (PathPredicate) isExpression() {}

type Comparison uint8

const (
	Equal Comparison = iota
	Greater
	Less
)

type SizePredicate struct {
	Comparison Comparison
	Count      uint64
	Unit       uint64
}

func (SizePredicate) isExpression() {}

type MTimePredicate struct {
	Comparison Comparison
	Days       int64
}

func (MTimePredicate) isExpression() {}

type NewerMTPredicate struct {
	Reference time.Time
}

func (NewerMTPredicate) isExpression() {}
