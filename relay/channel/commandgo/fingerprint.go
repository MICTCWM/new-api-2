package commandgo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// Device profile and fingerprint derivation ported from the Command Code Go
// prototype (src/protocol/fingerprint.mjs). Everything is derived
// deterministically from the upstream account key so the same account always
// presents the same machine to the upstream, across restarts and instances.
const (
	fingerprintSalt = "command-code:device-fingerprint:v1"
	devicePlatform  = "win32"
	deviceArch      = "x64"
	deviceOSRelease = "10.0.22631"
)

var fingerprintCPUModels = []string{
	"12th Gen Intel(R) Core(TM) i7-12650H",
	"12th Gen Intel(R) Core(TM) i5-12400F",
	"12th Gen Intel(R) Core(TM) i9-12900K",
	"13th Gen Intel(R) Core(TM) i7-13700K",
	"13th Gen Intel(R) Core(TM) i5-13600K",
	"13th Gen Intel(R) Core(TM) i9-13900K",
	"Intel(R) Core(TM) Ultra 7 155H",
	"Intel(R) Core(TM) Ultra 9 285H",
	"Intel(R) Core(TM) i9-14900K",
	"Intel(R) Core(TM) i7-14700K",
	"AMD Ryzen 7 7800X3D",
	"AMD Ryzen 9 7950X",
	"AMD Ryzen 5 7600",
	"AMD Ryzen 9 7900X",
	"AMD Ryzen 7 5800X3D",
}

var fingerprintCPUCores = []int{10, 6, 16, 16, 14, 24, 16, 16, 24, 20, 8, 16, 6, 12, 8}
var fingerprintMemGiB = []int{8, 16, 24, 32, 48, 64}
var fingerprintTimezones = []string{
	"America/New_York", "America/Chicago", "America/Los_Angeles", "America/Toronto",
	"Europe/London", "Europe/Berlin", "Europe/Paris", "Europe/Moscow",
	"Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "Asia/Seoul", "Asia/Hong_Kong",
	"Australia/Sydney", "Pacific/Auckland",
}
var fingerprintMACCounts = []int{2, 3, 4, 5}
var fingerprintOSUsers = []string{"dev", "user", "admin", "coder", "engineer", "work"}
var fingerprintMailDomains = []string{"gmail.com", "outlook.com", "qq.com", "163.com"}
var fingerprintProjectNames = []string{"app", "workspace", "project", "repo", "service", "sandbox"}

// DeviceProfile is the forged device identity used both for the request
// envelope and for x-project-slug.
type DeviceProfile struct {
	Platform   string
	Arch       string
	OSRelease  string
	ProjectDir string
}

// Fingerprint mirrors the CLI device fingerprint shape.
type Fingerprint struct {
	Thumbmark  string
	Components FingerprintComponents
}

// FingerprintComponents carries the hashed machine signals.
type FingerprintComponents struct {
	MachineIDHash string
	MACHashes     []string
	OSUserHash    string
	HostnameHash  string
	GitEmailHash  string
	Platform      string
	Arch          string
	OSRelease     string
	CPUModel      string
	CPUCount      int
	MemGiB        int
	IsContainer   bool
	Timezone      string
	Runtime       string
	CollectorVers int
}

// ProfileForAccount returns the deterministic forged device profile for an
// account key.
//
// TODO: the prototype also records the fingerprint via
// /alpha/fingerprint/record; this adaptor only needs the derived working
// directory and x-project-slug, so reporting is intentionally not wired yet.
func ProfileForAccount(apiKey string) DeviceProfile {
	osUser := fingerprintOSUsers[fpPickIndex(apiKey, "osUser", len(fingerprintOSUsers), func(i int) string {
		return fingerprintOSUsers[i]
	})]
	project := fingerprintProjectNames[fpPickIndex(apiKey, "project", len(fingerprintProjectNames), func(i int) string {
		return fingerprintProjectNames[i]
	})]
	return DeviceProfile{
		Platform:   devicePlatform,
		Arch:       deviceArch,
		OSRelease:  deviceOSRelease,
		ProjectDir: `C:\Users\` + osUser + `\projects\` + project,
	}
}

// GenerateFingerprint derives the deterministic device fingerprint for an
// account key.
func GenerateFingerprint(apiKey string) Fingerprint {
	cpuIdx := fpPickIndex(apiKey, "cpu", len(fingerprintCPUModels), func(i int) string {
		return fingerprintCPUModels[i]
	})
	memIdx := fpPickIndex(apiKey, "mem", len(fingerprintMemGiB), func(i int) string {
		return strconv.Itoa(fingerprintMemGiB[i])
	})
	tzIdx := fpPickIndex(apiKey, "timezone", len(fingerprintTimezones), func(i int) string {
		return fingerprintTimezones[i]
	})
	macCountIdx := fpPickIndex(apiKey, "macCount", len(fingerprintMACCounts), func(i int) string {
		return strconv.Itoa(fingerprintMACCounts[i])
	})
	osUserIdx := fpPickIndex(apiKey, "osUser", len(fingerprintOSUsers), func(i int) string {
		return fingerprintOSUsers[i]
	})
	mailIdx := fpPickIndex(apiKey, "mailDomain", len(fingerprintMailDomains), func(i int) string {
		return fingerprintMailDomains[i]
	})

	machineID := formatUUIDLike(hexOf(apiKey, "machineId", 16))
	macs := make([]string, 0, fingerprintMACCounts[macCountIdx])
	for i := 0; i < fingerprintMACCounts[macCountIdx]; i++ {
		macs = append(macs, formatMAC(fpDigest(apiKey, "mac"+strconv.Itoa(i))[:6]))
	}
	sort.Strings(macs)

	hostname := "DESKTOP-" + strings.ToUpper(hexOf(apiKey, "hostname", 4))
	gitEmail := fingerprintOSUsers[osUserIdx] + "." + hexOf(apiKey, "gitEmail", 3) + "@" + fingerprintMailDomains[mailIdx]

	thumbSeed := []string{machineID, strings.Join(macs, ",")}
	thumbmark := sha256Hex(fingerprintSalt + "\x00machine\x00" + strings.Join(thumbSeed, "|"))

	return Fingerprint{
		Thumbmark: thumbmark,
		Components: FingerprintComponents{
			MachineIDHash: fingerprintHash(machineID),
			MACHashes:     fingerprintHashes(macs),
			OSUserHash:    fingerprintHash(fingerprintOSUsers[osUserIdx]),
			HostnameHash:  fingerprintHash(hostname),
			GitEmailHash:  fingerprintHash(gitEmail),
			Platform:      devicePlatform,
			Arch:          deviceArch,
			OSRelease:     deviceOSRelease,
			CPUModel:      fingerprintCPUModels[cpuIdx],
			CPUCount:      fingerprintCPUCores[cpuIdx],
			MemGiB:        fingerprintMemGiB[memIdx],
			IsContainer:   false,
			Timezone:      fingerprintTimezones[tzIdx],
			Runtime:       "cli",
			CollectorVers: 1,
		},
	}
}

// SlugifyProjectPath reproduces the CLI's slugify(workingDir) rule: lowercase,
// collapse every non [a-z0-9] run to '-', trim dashes, fall back to "root".
func SlugifyProjectPath(path string) string {
	lower := strings.ToLower(path)
	var b strings.Builder
	lastDash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "root"
	}
	return slug
}

func fpDigest(apiKey, field string) []byte {
	sum := sha256.Sum256([]byte(fingerprintSalt + "\x00" + apiKey + "\x00" + field))
	return sum[:]
}

// fpPickIndex picks the candidate with the greatest digest, so adding a new
// candidate to the pool only affects keys where the new candidate wins.
func fpPickIndex(apiKey, field string, length int, labelOf func(int) string) int {
	bestIdx := 0
	var best []byte
	for i := 0; i < length; i++ {
		score := fpDigest(apiKey, field+"\x00"+labelOf(i))
		if best == nil || bytes.Compare(score, best) > 0 {
			best = score
			bestIdx = i
		}
	}
	return bestIdx
}

func fingerprintHash(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	return sha256Hex(fingerprintSalt + "\x00" + strings.ToLower(v))
}

func fingerprintHashes(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if h := fingerprintHash(v); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hexOf(apiKey, field string, n int) string {
	sum := fpDigest(apiKey, field)
	if n > len(sum) {
		n = len(sum)
	}
	return hex.EncodeToString(sum[:n])
}

func formatUUIDLike(hexValue string) string {
	if len(hexValue) != 32 {
		return hexValue
	}
	return hexValue[0:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}

func formatMAC(raw []byte) string {
	parts := make([]string, len(raw))
	for i, b := range raw {
		parts[i] = hex.EncodeToString([]byte{b})
	}
	return strings.Join(parts, ":")
}
