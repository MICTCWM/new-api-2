package commandgo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProfileForAccountIsDeterministic(t *testing.T) {
	a := ProfileForAccount("user_abc")
	b := ProfileForAccount("user_abc")
	assert.Equal(t, a, b)
	assert.Equal(t, devicePlatform, a.Platform)
	assert.Equal(t, deviceArch, a.Arch)
	assert.Contains(t, a.ProjectDir, `C:\Users\`)
	assert.Contains(t, a.ProjectDir, `\projects\`)
}

func TestProfileDiffersByAccount(t *testing.T) {
	seen := make(map[string]struct{})
	for _, key := range []string{"user_1", "user_2", "user_3", "user_4", "user_5", "user_6"} {
		seen[ProfileForAccount(key).ProjectDir] = struct{}{}
	}
	// The deterministic pool is small; at least two distinct machines must be
	// produced across six accounts.
	assert.Greater(t, len(seen), 1)
}

func TestGenerateFingerprintIsDeterministic(t *testing.T) {
	a := GenerateFingerprint("user_abc")
	b := GenerateFingerprint("user_abc")
	assert.Equal(t, a, b)
	assert.NotEmpty(t, a.Thumbmark)
	assert.Equal(t, devicePlatform, a.Components.Platform)
	assert.NotEmpty(t, a.Components.MachineIDHash)
	assert.NotEmpty(t, a.Components.HostnameHash)
	assert.GreaterOrEqual(t, len(a.Components.MACHashes), 2)
}
