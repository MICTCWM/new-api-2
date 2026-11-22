package commandgo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NotEmpty(t, a.Thumbmark)
	assert.Equal(t, devicePlatform, a.Components.Platform)
	require.NotEmpty(t, a.Components.MachineIDHash)
	require.NotEmpty(t, a.Components.HostnameHash)
	require.GreaterOrEqual(t, len(a.Components.MACHashes), 2)
}

func TestGenerateFingerprintDiffersByAccount(t *testing.T) {
	a := GenerateFingerprint("user_aaa")
	b := GenerateFingerprint("user_bbb")
	assert.NotEqual(t, a.Thumbmark, b.Thumbmark)
	assert.NotEqual(t, a.Components.MachineIDHash, b.Components.MachineIDHash)
}

// TestGenerateFingerprintMatchesPrototypeVector re-implements the prototype
// algorithm independently (fingerprint.mjs) and asserts the Go result field for
// field. This is the hash-alignment guard: any drift in the derivation salt,
// CPU label, signal salt, or thumbmark seed shows up here.
func TestGenerateFingerprintMatchesPrototypeVector(t *testing.T) {
	const key = "user_hash_vector_01"

	digest := func(field string) []byte {
		sum := sha256.Sum256([]byte("" + "\x00" + key + "\x00" + field))
		return sum[:]
	}
	hexOf := func(field string, n int) string { return hex.EncodeToString(digest(field)[:n]) }
	pick := func(field string, length int, labelOf func(int) string) int {
		best := 0
		var bestScore []byte
		for i := 0; i < length; i++ {
			sum := sha256.Sum256([]byte("" + "\x00" + key + "\x00" + field + "\x00" + labelOf(i)))
			score := sum[:]
			if bestScore == nil || bytes.Compare(score, bestScore) > 0 {
				bestScore = score
				best = i
			}
		}
		return best
	}
	signalHash := func(value string) string {
		v := strings.TrimSpace(value)
		if v == "" {
			return ""
		}
		sum := sha256.Sum256([]byte(fingerprintSignalSalt + "\x00" + strings.ToLower(v)))
		return hex.EncodeToString(sum[:])
	}

	cpuIdx := pick("cpu", len(fingerprintCPUModels), func(i int) string {
		return fingerprintCPUModels[i] + "|" + strconv.Itoa(fingerprintCPUCores[i])
	})
	macCountIdx := pick("macCount", len(fingerprintMACCounts), func(i int) string {
		return strconv.Itoa(fingerprintMACCounts[i])
	})
	osUserIdx := pick("osUser", len(fingerprintOSUsers), func(i int) string {
		return fingerprintOSUsers[i]
	})
	mailIdx := pick("mailDomain", len(fingerprintMailDomains), func(i int) string {
		return fingerprintMailDomains[i]
	})
	memIdx := pick("mem", len(fingerprintMemGiB), func(i int) string {
		return strconv.Itoa(fingerprintMemGiB[i])
	})
	tzIdx := pick("timezone", len(fingerprintTimezones), func(i int) string {
		return fingerprintTimezones[i]
	})

	mid := hexOf("machineId", 16)
	machineID := mid[0:8] + "-" + mid[8:12] + "-" + mid[12:16] + "-" + mid[16:20] + "-" + mid[20:32]
	macs := make([]string, 0, fingerprintMACCounts[macCountIdx])
	for i := 0; i < fingerprintMACCounts[macCountIdx]; i++ {
		macs = append(macs, formatMAC(digest("mac" + strconv.Itoa(i))[:6]))
	}
	sort.Strings(macs)
	hostname := "DESKTOP-" + strings.ToUpper(hexOf("hostname", 4))
	gitEmail := fingerprintOSUsers[osUserIdx] + "." + hexOf("gitEmail", 3) + "@" + fingerprintMailDomains[mailIdx]
	thumbSeed := machineID + "|" + strings.Join(macs, ",")
	thumbSum := sha256.Sum256([]byte(fingerprintSignalSalt + "\x00machine\x00" + thumbSeed))

	got := GenerateFingerprint(key)
	require.NotEmpty(t, got.Thumbmark)
	require.Len(t, got.Components.MACHashes, len(macs))
	assert.Equal(t, hex.EncodeToString(thumbSum[:]), got.Thumbmark)
	assert.Equal(t, machineID, formatUUIDLike(hexOf("machineId", 16)))
	assert.Equal(t, signalHash(machineID), got.Components.MachineIDHash)
	assert.Equal(t, signalHash(hostname), got.Components.HostnameHash)
	assert.Equal(t, signalHash(gitEmail), got.Components.GitEmailHash)
	assert.Equal(t, signalHash(fingerprintOSUsers[osUserIdx]), got.Components.OSUserHash)
	for i, mac := range macs {
		assert.Equal(t, signalHash(mac), got.Components.MACHashes[i])
	}
	assert.Equal(t, fingerprintCPUModels[cpuIdx], got.Components.CPUModel)
	assert.Equal(t, fingerprintCPUCores[cpuIdx], got.Components.CPUCount)
	assert.Equal(t, fingerprintMemGiB[memIdx], got.Components.MemGiB)
	assert.Equal(t, fingerprintTimezones[tzIdx], got.Components.Timezone)
}

// TestFingerprintSignalSaltIndependentOfDerivationSalt ensures the two salts
// stay separate: changing the derivation salt rotates the forged machine but
// must not change how signal values are hashed.
func TestFingerprintSignalSaltIndependentOfDerivationSalt(t *testing.T) {
	t.Cleanup(func() { SetFingerprintDerivationSalt("") })

	before := GenerateFingerprint("user_salt_check")
	SetFingerprintDerivationSalt("rotate-2026")
	after := GenerateFingerprint("user_salt_check")
	assert.NotEqual(t, before.Components.MachineIDHash, after.Components.MachineIDHash)
	assert.NotEqual(t, before.Thumbmark, after.Thumbmark)

	// Signal hashing is pinned to the CLI salt regardless of the derivation salt.
	assert.Equal(t,
		sha256Hex(fingerprintSignalSalt+"\x00"+"desktop-abc"),
		fingerprintHash("  DESKTOP-ABC  "))
}
