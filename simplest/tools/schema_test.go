package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

const testSchema = `{
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "count": { "type": "number", "minimum": 1 },
    "mode": { "enum": ["fast", "slow"] },
    "tags": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": { "name": { "type": "string" } },
        "required": ["name"],
        "additionalProperties": false
      }
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

func validate(t *testing.T, args string) error {
	t.Helper()
	_, err := ValidateAgainstSchema(json.RawMessage(args), json.RawMessage(testSchema))
	return err
}

func TestValidateAgainstSchemaValid(t *testing.T) {
	if err := validate(t, `{"path":"a.go","count":2,"mode":"fast","tags":[{"name":"x"}]}`); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
}

func TestValidateAgainstSchemaRequiredMissing(t *testing.T) {
	err := validate(t, `{"count":1}`)
	if err == nil || !strings.Contains(err.Error(), "missing required argument: path") {
		t.Errorf("err = %v, want missing required argument: path", err)
	}
}

func TestValidateAgainstSchemaWrongType(t *testing.T) {
	tests := []struct{ name, args, wantSub string }{
		{"string expected", `{"path":123}`, "path must be of type string"},
		{"number expected", `{"path":"a","count":"many"}`, "count must be of type number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(t, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("err = %v, want containing %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidateAgainstSchemaAdditionalPropertiesFalse(t *testing.T) {
	err := validate(t, `{"path":"a.go","bogus":true}`)
	if err == nil || !strings.Contains(err.Error(), "unknown argument: bogus") {
		t.Errorf("err = %v, want unknown argument: bogus", err)
	}
}

func TestValidateAgainstSchemaEnumViolation(t *testing.T) {
	err := validate(t, `{"path":"a.go","mode":"turbo"}`)
	if err == nil || !strings.Contains(err.Error(), "mode must be one of") || !strings.Contains(err.Error(), `"fast", "slow"`) {
		t.Errorf("err = %v, want enum violation listing fast, slow", err)
	}
}

func TestValidateAgainstSchemaNestedArrayItems(t *testing.T) {
	err := validate(t, `{"path":"a.go","tags":[{"name":"ok"},{"nope":1}]}`)
	if err == nil {
		t.Fatal("expected nested item validation error")
	}
	for _, sub := range []string{"tags[1].name", "missing required argument"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q missing %q", err.Error(), sub)
		}
	}

	err = validate(t, `{"path":"a.go","tags":[{"name":"ok"},{"name":42}]}`)
	if err == nil || !strings.Contains(err.Error(), "tags[1].name must be of type string") {
		t.Errorf("err = %v, want nested wrong-type error at tags[1].name", err)
	}
}

func TestValidateAgainstSchemaNumericConstraints(t *testing.T) {
	if err := validate(t, `{"path":"a","count":0}`); err == nil || !strings.Contains(err.Error(), "count must be >= 1") {
		t.Errorf("err = %v, want minimum violation", err)
	}
}

func TestBuiltinToolSchemasAcceptTheirOwnArgs(t *testing.T) {
	dir := t.TempDir()
	r := DefaultRegistry(dir)
	valid := map[string]string{
		"read":  `{"path":"f.txt"}`,
		"bash":  `{"command":"true"}`,
		"write": `{"path":"f.txt","content":""}`,
		"edit":  `{"path":"f.txt","edits":[{"oldText":"a","newText":"b"}]}`,
		"find":  `{"pattern":"*.go"}`,
		"grep":  `{"pattern":"x"}`,
		"ls":    `{}`,
	}
	for name, args := range valid {
		tool, ok := r.Get(name)
		if !ok {
			continue
		}
		if _, err := ValidateAgainstSchema(json.RawMessage(args), tool.Parameters()); err != nil {
			t.Errorf("%s schema rejects its own args %s: %v", name, args, err)
		}
		bad := strings.TrimSuffix(args, "}") + `,"hacker":1}`
		if _, err := ValidateAgainstSchema(json.RawMessage(bad), tool.Parameters()); err == nil {
			t.Errorf("%s schema should reject unknown key in %s", name, bad)
		}
	}
}
