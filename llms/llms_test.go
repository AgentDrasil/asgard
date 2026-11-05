package llms

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusUsage(t *testing.T) {
	u := StatusUsage(errors.New("agy /usage: Authentication required"))
	assert.Equal(t, QuotaStatusNoCredentials, u.Status)
	assert.Equal(t, "agy /usage: Authentication required", u.Message)
	assert.True(t, u.IsStatusOnly())
	assert.Empty(t, u.Model)

	u = StatusUsage(errors.New("bwrap: setting up uid map: Permission denied"))
	assert.Equal(t, QuotaStatusError, u.Status)

	assert.True(t, IsNoCredentialsText("Not logged in - Please run /login"))
	assert.False(t, ModelUsage{Model: "m"}.IsStatusOnly())
}
