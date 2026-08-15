package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// exprPattern matches a single comparison of the form
// `nodes.<id>.<field> <op> <literal>` using dot notation.
var exprPattern = regexp.MustCompile(`^\s*([A-Za-z0-9_\-.\[\]]+)\s*(==|!=|>=|<=|>|<)\s*(\S(?:.*\S)?)\s*$`)

// Interpolate expands ${...} placeholders in text. Unknown keys are left
// untouched so that shell variables like ${HOME} pass through verbatim.
func Interpolate(text string, resolve func(key string) (string, bool)) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		rest := text[i:]
		if !strings.HasPrefix(rest, "${") {
			b.WriteByte(text[i])
			i++
			continue
		}
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			b.WriteString(rest)
			break
		}
		key := rest[2:end]
		if v, ok := resolve(key); ok {
			b.WriteString(v)
		} else {
			b.WriteString(rest[:end+1])
		}
		i += end + 1
	}
	return b.String()
}

// EvaluateSimpleExpr evaluates a `when` condition such as
// `nodes.build_cmd.exit_code != 0` or `nodes.build_cmd.status == 'FAILED'`
// against the settled upstream node results.
//
// A nil upstreams map enables syntax-check-only mode: the expression structure
// is validated and (true, nil) is returned without resolving node references.
func EvaluateSimpleExpr(expr string, upstreams map[string]*NodeResult, defn *WorkflowDefinition) (bool, error) {
	m := exprPattern.FindStringSubmatch(expr)
	if m == nil {
		return false, fmt.Errorf("invalid expression %q: expected form `nodes.<id>.<field> <op> <literal>`", expr)
	}
	leftPath, op, rawLiteral := m[1], m[2], m[3]

	if upstreams == nil {
		if _, err := parseLiteral(rawLiteral); err != nil {
			return false, fmt.Errorf("invalid literal in %q: %w", expr, err)
		}
		return true, nil
	}

	left, err := resolveNodeValue(leftPath, upstreams, defn)
	if err != nil {
		return false, err
	}
	literal, err := parseLiteral(rawLiteral)
	if err != nil {
		return false, fmt.Errorf("invalid literal in %q: %w", expr, err)
	}

	// Numeric comparison when both sides are integers.
	leftNum, leftNumErr := strconv.Atoi(strings.TrimSpace(left))
	litNum, litNumErr := strconv.Atoi(strings.TrimSpace(literal))
	if leftNumErr == nil && litNumErr == nil {
		return compareOrdered(op, leftNum, litNum)
	}

	switch op {
	case "==":
		return strings.TrimSpace(left) == strings.TrimSpace(literal), nil
	case "!=":
		return strings.TrimSpace(left) != strings.TrimSpace(literal), nil
	}
	return false, fmt.Errorf("operator %q only supports numeric comparison (left=%q is not a number)", op, left)
}

func compareOrdered(op string, a, b int) (bool, error) {
	switch op {
	case "==":
		return a == b, nil
	case "!=":
		return a != b, nil
	case ">":
		return a > b, nil
	case ">=":
		return a >= b, nil
	case "<":
		return a < b, nil
	case "<=":
		return a <= b, nil
	}
	return false, fmt.Errorf("unsupported operator %q", op)
}

// parseLiteral strips surrounding quotes from the raw literal.
func parseLiteral(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty literal")
	}
	if len(raw) >= 2 {
		first, last := raw[0], raw[len(raw)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			return raw[1 : len(raw)-1], nil
		}
	}
	return raw, nil
}

// resolveNodeValue resolves a dot-notation path against node results.
// Supported paths: nodes.<id>.status | exit_code | output | error | skip_reason
// plus spec fields such as nodes.<id>.output_file (resolved via defn).
func resolveNodeValue(path string, upstreams map[string]*NodeResult, defn *WorkflowDefinition) (string, error) {
	parts := strings.Split(path, ".")
	if len(parts) < 3 || parts[0] != "nodes" {
		return "", fmt.Errorf("path %q must start with nodes.<id>.<field>", path)
	}
	nodeID := parts[1]
	field := strings.Join(parts[2:], ".")

	res, ok := upstreams[nodeID]
	if !ok || res == nil {
		return "", fmt.Errorf("when expression references node %q which has not settled yet", nodeID)
	}

	switch field {
	case "status":
		return string(res.Status), nil
	case "exit_code":
		return strconv.Itoa(res.ExitCode), nil
	case "output":
		return res.Output, nil
	case "skip_reason":
		return string(res.SkipReason), nil
	case "error":
		if res.Error != nil {
			return res.Error.Error(), nil
		}
		return "", nil
	}

	// Fall back to spec fields on the definition (e.g. output_file).
	if defn != nil {
		for _, node := range defn.Nodes {
			if node.ID != nodeID {
				continue
			}
			switch field {
			case "output_file":
				return node.OutputFile, nil
			case "agent_id":
				return node.AgentID, nil
			case "type":
				return string(node.Type), nil
			}
		}
	}
	return "", fmt.Errorf("unknown field %q on node %q", field, nodeID)
}
