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
	OpencodeConcurrencyLimitOptionKey = "OpencodeConcurrencyLimit"
	OpencodeRPMLimitOptionKey         = "OpencodeRPMLimit" // legacy option key
	DefaultOpencodeConcurrencyLimit   = 3
	MinOpencodeConcurrencyLimit       = 1
	MaxOpencodeConcurrencyLimit       = 1000
	DefaultOpencodeRPMLimit           = DefaultOpencodeConcurrencyLimit
	MinOpencodeRPMLimit               = MinOpencodeConcurrencyLimit
	MaxOpencodeRPMLimit               = MaxOpencodeConcurrencyLimit
)

var opencodeConcurrencyLimit atomic.Int64

func init() {
	opencodeConcurrencyLimit.Store(DefaultOpencodeConcurrencyLimit)
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
