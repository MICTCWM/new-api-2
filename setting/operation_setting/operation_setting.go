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
	OpencodeRPMLimitOptionKey = "OpencodeRPMLimit"
	DefaultOpencodeRPMLimit   = 3
	MinOpencodeRPMLimit       = 1
	MaxOpencodeRPMLimit       = 1000
)

var opencodeRPMLimit atomic.Int64

func init() {
	opencodeRPMLimit.Store(DefaultOpencodeRPMLimit)
}

func GetOpencodeRPMLimit() int {
	return int(opencodeRPMLimit.Load())
}

func ValidateOpencodeRPMLimit(value string) error {
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || limit < MinOpencodeRPMLimit || limit > MaxOpencodeRPMLimit {
		return fmt.Errorf("OpenCode RPM must be an integer from %d to %d", MinOpencodeRPMLimit, MaxOpencodeRPMLimit)
	}
	return nil
}

func SetOpencodeRPMLimit(value string) error {
	if err := ValidateOpencodeRPMLimit(value); err != nil {
		return err
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(value))
	opencodeRPMLimit.Store(int64(limit))
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
