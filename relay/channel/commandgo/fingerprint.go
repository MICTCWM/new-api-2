package commandgo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// Device profile and fingerprint derivation ported from the Command Code Go
// prototype (src/protocol/fingerprint.mjs). Everything is derived
// deterministically from the upstream account key so the same account always
// presents the same machine to the upstream, across restarts and instances.
const (
	// fingerprintSignalSalt is the CLI's fixed root salt used to hash individual
	// machine signals (fingerprint.mjs FP_SALT). It is NOT the derivation salt.
	fingerprintSignalSalt = "command-code:device-fingerprint:v1"
	devicePlatform        = "win32"
	deviceArch            = "x64"
	deviceOSRelease       = "10.0.22631"
)

// fingerprintDerivationSalt mirrors CFG.fingerprintSalt in the prototype: it
// only affects *which* forged machine a key maps to, never the signal hashes.
// Defaults to empty like the prototype. Configure it once at startup.
var fingerprintDerivationSalt atomic.Value // string

func init() {
	fingerprintDerivationSalt.Store("")
}

func currentFingerprintDerivationSalt() string {
	return fingerprintDerivationSalt.Load().(string)
}

// SetFingerprintDerivationSalt lets callers override the derivation salt
// without touching the signal-hash salt. An empty value restores the default.
// Intended for startup/test use only: cached fingerprints are derived from the
// active salt, so rotating at runtime is unsupported. This drops every cached
// fingerprint so the next use re-derives with the new salt.
func SetFingerprintDerivationSalt(salt string) {
	fingerprintDerivationSalt.Store(salt)
	clearFingerprintStates()
}

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

// Fingerprint mirrors the CLI device fingerprint shape reported to
// POST /alpha/fingerprint/record. JSON tags match the prototype payload.
type Fingerprint struct {
	Thumbmark  string                `json:"thumbmark"`
	Components FingerprintComponents `json:"components"`
}

// FingerprintComponents carries the hashed machine signals. Field names match
// the upstream report body exactly. Hash fields are effectively always non-empty
// for a real key (machine id/hostname/git email are key-derived); omitempty only
// mirrors the prototype's "undefined" fields in the degenerate empty case.
// IsContainer is intentionally NOT omitempty: the prototype always sends
// isContainer=false.
type FingerprintComponents struct {
	MachineIDHash string   `json:"machineIdHash,omitempty"`
	MACHashes     []string `json:"macHashes,omitempty"`
	OSUserHash    string   `json:"osUserHash,omitempty"`
	HostnameHash  string   `json:"hostnameHash,omitempty"`
	GitEmailHash  string   `json:"gitEmailHash,omitempty"`
	Platform      string   `json:"platform"`
	Arch          string   `json:"arch"`
	OSRelease     string   `json:"osRelease"`
	CPUModel      string   `json:"cpuModel"`
	CPUCount      int      `json:"cpuCount"`
	MemGiB        int      `json:"memGiB"`
	IsContainer   bool     `json:"isContainer"`
	Timezone      string   `json:"timezone"`
	Runtime       string   `json:"runtime"`
	CollectorVers int      `json:"collectorVersion"`
}

// ProfileForAccount returns the deterministic forged device profile for an
// account key.
//
// The prototype keeps a single global projectDir for the whole process. Here it
// is derived per key on purpose: it must stay on the same forged machine as
// osUser (which also comes from the key), so every account presents one
// self-consistent machine rather than sharing a process-wide identity.
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
// account key. It mirrors generateFingerprint in fingerprint.mjs field for
// field, including the CPU candidate label ("model|cores") and the thumbmark
// seed construction.
func GenerateFingerprint(apiKey string) Fingerprint {
	cpuIdx := fpPickIndex(apiKey, "cpu", len(fingerprintCPUModels), func(i int) string {
		return fingerprintCPUModels[i] + "|" + strconv.Itoa(fingerprintCPUCores[i])
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
	// JS Array.prototype.sort on MAC strings is lexicographic (code unit order),
	// which matches Go's sort.Strings for lowercase hex.
	sort.Strings(macs)

	hostname := "DESKTOP-" + strings.ToUpper(hexOf(apiKey, "hostname", 4))
	gitEmail := fingerprintOSUsers[osUserIdx] + "." + hexOf(apiKey, "gitEmail", 3) + "@" + fingerprintMailDomains[mailIdx]

	// thumbmark seed: [machineId, macs.join(",")] and, only when machineId is
	// empty, hostname then cpuModel. Empty seed falls back to "unknown".
	trimmedMachineID := strings.TrimSpace(machineID)
	seedParts := make([]string, 0, 4)
	seedParts = append(seedParts, trimmedMachineID, strings.Join(macs, ","))
	if trimmedMachineID == "" {
		seedParts = append(seedParts, hostname, fingerprintCPUModels[cpuIdx])
	}
	seed := "unknown"
	nonEmptyParts := seedParts[:0]
	for _, part := range seedParts {
		if strings.TrimSpace(part) != "" {
			nonEmptyParts = append(nonEmptyParts, part)
		}
	}
	if joined := strings.Join(nonEmptyParts, "|"); joined != "" {
		seed = joined
	}
	thumbmark := sha256Hex(fingerprintSignalSalt + "\x00machine\x00" + seed)

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

// fpDigest is the derivation digest used to pick forged machine signals. It
// uses the configurable derivation salt (default empty), NOT the CLI signal
// salt — matching prototype fpDigest.
func fpDigest(apiKey, field string) []byte {
	sum := sha256.Sum256([]byte(currentFingerprintDerivationSalt() + "\x00" + apiKey + "\x00" + field))
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

// fingerprintHash is the CLI hashSignal: sha256(FP_SALT + "\0" + value lowered
// and trimmed); empty values yield "" (omitted from the report like undefined).
func fingerprintHash(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	return sha256Hex(fingerprintSignalSalt + "\x00" + strings.ToLower(v))
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
