package output

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lenovobenben/panfind/internal/namespace"
)

// Printf formats one query result using PanFind's supported GNU find subset.
// Supported directives are %p, %f, %s, %y, %T+, and %%.
func Printf(format, cloudPath string, node namespace.Node) (string, error) {
	var result strings.Builder
	for i := 0; i < len(format); i++ {
		switch format[i] {
		case '\\':
			if i+1 >= len(format) {
				return "", fmt.Errorf("trailing backslash in -printf format")
			}
			i++
			switch format[i] {
			case 'n':
				result.WriteByte('\n')
			case 't':
				result.WriteByte('\t')
			case '0':
				result.WriteByte(0)
			case '\\':
				result.WriteByte('\\')
			default:
				return "", fmt.Errorf("unsupported -printf escape \\%c", format[i])
			}
		case '%':
			if i+1 >= len(format) {
				return "", fmt.Errorf("trailing %% in -printf format")
			}
			i++
			switch format[i] {
			case '%':
				result.WriteByte('%')
			case 'p':
				result.WriteString(cloudPath)
			case 'f':
				result.WriteString(node.Name)
			case 's':
				result.WriteString(strconv.FormatUint(node.Size, 10))
			case 'y':
				result.WriteString(nodeTypeLetter(node.Kind))
			case 'T':
				if i+1 >= len(format) || format[i+1] != '+' {
					return "", fmt.Errorf("only %%T+ is supported for modification time")
				}
				i++
				if node.ModifiedAt != nil {
					result.WriteString(node.ModifiedAt.Format("2006-01-02T15:04:05.000000000Z07:00"))
				}
			default:
				return "", fmt.Errorf("unsupported -printf directive %%%c", format[i])
			}
		default:
			result.WriteByte(format[i])
		}
	}
	return result.String(), nil
}

func nodeTypeLetter(kind namespace.NodeKind) string {
	switch kind {
	case namespace.NodeKindFile:
		return "f"
	case namespace.NodeKindDirectory:
		return "d"
	default:
		return "?"
	}
}
