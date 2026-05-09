---
trigger: model_decision
description: when writing go tests
---

## Go Test

- go test should use stretchr/testify lib. Use require for setup and prerequisites; use assert for result verification.
- Any test helper functions must call t.Helper() at the beginning.
- Prefer t.Cleanup() over defer for resource teardown to ensure clean execution order.
- Be specific about errors. Use assert.ErrorIs or assert.ErrorContains where appropriate.
- Prefer manually written stubs or fakes over reflection-based mocking frameworks, eg. testify/mock.
- For infrastructure (DB, FileSystem, Cache), prefer real implementations with isolated environments over mocks whenever possible.
- When need to use temp dir, you should use t.TempDir().
- Call t.Parallel() at the start of the test and inside t.Run for independent cases.
  - easy to trigger race condition
  - fast
- Prefer table tests

```go
func TestExample_TableDriven(t *testing.T) {
    t.Parallel()

    tempDir := t.TempDir()

    tests := []struct {
        name    string
        input   string
        wantErr bool
    }{
        {"valid input", "data", false},
        {"empty input", "", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()

            res, err := YourFunc(tt.input, tempDir)

            if tt.wantErr {
                require.Error(t, err)
                return
            }

            require.NoError(t, err)
            assert.Equal(t, "expected", res)
        })
    }
}
```