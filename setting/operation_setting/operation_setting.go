package operation_setting

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

var DemoSiteEnabled = false
var SelfUseModeEnabled = false

const (
	OpencodeConcurrencyLimitOptionKey      = "OpencodeConcurrencyLimit"
	OpencodeRPMLimitOptionKey              = "OpencodeRPMLimit" // legacy option key
	OpencodeSessionRetentionOptionKey      = "OpencodeSessionRetentionSeconds"
	DefaultOpencodeConcurrencyLimit        = 3
	MinOpencodeConcurrencyLimit            = 1
	MaxOpencodeConcurrencyLimit            = 1000
	DefaultOpencodeSessionRetentionSeconds = 30 * 60
	MinOpencodeSessionRetentionSeconds     = 60
	MaxOpencodeSessionRetentionSeconds     = 7 * 24 * 60 * 60
	DefaultOpencodeRPMLimit                = DefaultOpencodeConcurrencyLimit
	MinOpencodeRPMLimit                    = MinOpencodeConcurrencyLimit
	MaxOpencodeRPMLimit                    = MaxOpencodeConcurrencyLimit
)

var opencodeConcurrencyLimit atomic.Int64
var opencodeSessionRetentionSeconds atomic.Int64

func init() {
	opencodeConcurrencyLimit.Store(DefaultOpencodeConcurrencyLimit)
	opencodeSessionRetentionSeconds.Store(DefaultOpencodeSessionRetentionSeconds)
}

func GetOpencodeConcurrencyLimit() int {
	return int(opencodeConcurrencyLimit.Load())
}

func ValidateOpencodeConcurrencyLimit(value string) error {
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || limit < MinOpencodeConcurrencyLimit || limit > MaxOpencodeConcurrencyLimit {
		return fmt.Errorf("OpenCode concurrency must be an integer from %d to %d", MinOpencodeConcurrencyLimit, MaxOpencodeConcurrencyLimit)
	}
	return nil
}

func SetOpencodeConcurrencyLimit(value string) error {
	if err := ValidateOpencodeConcurrencyLimit(value); err != nil {
		return err
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(value))
	opencodeConcurrencyLimit.Store(int64(limit))
	return nil
}

// GetOpencodeRPMLimit is kept for callers and stored options from the RPM
// implementation. Runtime routing uses the concurrency limit.
func GetOpencodeRPMLimit() int {
	return GetOpencodeConcurrencyLimit()
}

func ValidateOpencodeRPMLimit(value string) error {
	return ValidateOpencodeConcurrencyLimit(value)
}

func SetOpencodeRPMLimit(value string) error {
	return SetOpencodeConcurrencyLimit(value)
}

func GetOpencodeSessionRetentionSeconds() int {
	return int(opencodeSessionRetentionSeconds.Load())
}

func ValidateOpencodeSessionRetentionSeconds(value string) error {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < MinOpencodeSessionRetentionSeconds || seconds > MaxOpencodeSessionRetentionSeconds {
		return fmt.Errorf("OpenCode session retention must be an integer from %d to %d seconds", MinOpencodeSessionRetentionSeconds, MaxOpencodeSessionRetentionSeconds)
	}
	return nil
}

func SetOpencodeSessionRetentionSeconds(value string) error {
	if err := ValidateOpencodeSessionRetentionSeconds(value); err != nil {
		return err
	}
	seconds, _ := strconv.Atoi(strings.TrimSpace(value))
	opencodeSessionRetentionSeconds.Store(int64(seconds))
	return nil
}

var AutomaticDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
}

func AutomaticDisableKeywordsToString() string {
	return strings.Join(AutomaticDisableKeywords, "\n")
}

func AutomaticDisableKeywordsFromString(s string) {
	AutomaticDisableKeywords = []string{}
	ak := strings.Split(s, "\n")
	for _, k := range ak {
		k = strings.TrimSpace(k)
		k = strings.ToLower(k)
		if k != "" {
			AutomaticDisableKeywords = append(AutomaticDisableKeywords, k)
		}
	}
}
