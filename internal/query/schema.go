package query

type Schema struct {
	Version    int             `json:"version"`
	Predicates []PredicateSpec `json:"predicates"`
	Operators  []OperatorSpec  `json:"operators"`
	Options    []OptionSpec    `json:"options"`
	Actions    []ActionSpec    `json:"actions"`
}

type PredicateSpec struct {
	Name      string   `json:"name"`
	Arguments []string `json:"arguments"`
	Field     string   `json:"field"`
	Semantics string   `json:"semantics"`
}

type OperatorSpec struct {
	Names      []string `json:"names"`
	Precedence int      `json:"precedence"`
	Arity      int      `json:"arity"`
}

type OptionSpec struct {
	Name      string `json:"name"`
	Argument  string `json:"argument"`
	Semantics string `json:"semantics"`
}

type ActionSpec struct {
	Name       string   `json:"name"`
	Arguments  []string `json:"arguments,omitempty"`
	Directives []string `json:"directives,omitempty"`
}

func LanguageSchema() Schema {
	return Schema{
		Version: 1,
		Predicates: []PredicateSpec{
			{Name: "-type", Arguments: []string{"f|d"}, Field: "kind", Semantics: "file or directory node kind"},
			{Name: "-name", Arguments: []string{"PATTERN"}, Field: "name", Semantics: "case-sensitive shell glob against basename"},
			{Name: "-iname", Arguments: []string{"PATTERN"}, Field: "name", Semantics: "case-insensitive shell glob against basename"},
			{Name: "-path", Arguments: []string{"PATTERN"}, Field: "path", Semantics: "case-sensitive glob against the printed provider path; wildcards cross directory separators"},
			{Name: "-ipath", Arguments: []string{"PATTERN"}, Field: "path", Semantics: "case-insensitive glob against the printed provider path; wildcards cross directory separators"},
			{Name: "-size", Arguments: []string{"[+-]N[cwbkMG]"}, Field: "size", Semantics: "GNU find unit rounding and comparison semantics"},
			{Name: "-mtime", Arguments: []string{"[+-]DAYS"}, Field: "modified_at", Semantics: "complete 24-hour periods relative to query time"},
			{Name: "-newermt", Arguments: []string{"DATE"}, Field: "modified_at", Semantics: "strictly newer than local date, local date-time, or RFC3339 timestamp"},
		},
		Operators: []OperatorSpec{
			{Names: []string{"!", "-not"}, Precedence: 3, Arity: 1},
			{Names: []string{"implicit", "-a", "-and"}, Precedence: 2, Arity: 2},
			{Names: []string{"-o", "-or"}, Precedence: 1, Arity: 2},
		},
		Options: []OptionSpec{
			{Name: "-mindepth", Argument: "N", Semantics: "minimum depth relative to the query root; must precede predicates"},
			{Name: "-maxdepth", Argument: "N", Semantics: "maximum depth relative to the query root; must precede predicates"},
		},
		Actions: []ActionSpec{
			{Name: "-print"},
			{Name: "-printf", Arguments: []string{"FORMAT"}, Directives: []string{"%p", "%f", "%s", "%y", "%T+", "%%", `\n`, `\t`, `\0`, `\\`}},
		},
	}
}
