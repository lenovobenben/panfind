package query

import "testing"

func TestExplain(t *testing.T) {
	parsed, err := Parse("baidu:/shows", []string{"-maxdepth", "2", "-type", "f", "-size", "+1G"})
	if err != nil {
		t.Fatal(err)
	}
	explanation, err := Explain(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Root != "baidu:/shows" || explanation.MaxDepth == nil || *explanation.MaxDepth != 2 {
		t.Fatalf("Explain() = %+v", explanation)
	}
	if explanation.Expression.Operator != "and" || explanation.Expression.Right == nil || explanation.Expression.Right.Operator != "size" {
		t.Fatalf("unexpected expression explanation: %+v", explanation.Expression)
	}
}
