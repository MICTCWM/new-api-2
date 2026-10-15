// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// internalResponseKeys are gateway-only fields (billing, channel identity, the
// operator's own cache counters). They are not part of any OpenAI-compatible
// schema and must never reach a downstream client. Mirrors INTERNAL_KEY_BLACKLIST
// in openai-response-sanitizer/sanitize-openai-response.mjs, minus the two
// counters that are official in the Claude schema (see openAIOnlyInternalKey).
var internalResponseKeys = [...]string{
	"credit",
	"provider",
	"prompt_cache_hit_tokens",
	"prompt_cache_miss_tokens",
	"prompt_cache_write_tokens",
	"completion_thinking_tokens",
}

// The two cache_* counters are official Claude usage fields, so they are only
// internal for OpenAI-family payloads. Keyed separately from the base set.
func openAIOnlyInternalKey(key string) bool {
	return key == "cache_creation_input_tokens" || key == "cache_read_input_tokens"
}

func isInternalResponseKey(key string, openAIFamily bool) bool {
	if openAIFamily && openAIOnlyInternalKey(key) {
		return true
	}
	for _, candidate := range internalResponseKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

// containsInternalResponseKey is a raw-text prefilter: payloads without any
// blacklisted key literal keep their original bytes and pay no re-parse cost.
// A literal found inside a string value is a false positive, which is harmless
// because the path walk below is key-position aware.
func containsInternalResponseKey(data []byte) bool {
	for _, key := range internalResponseKeys {
		if bytes.Contains(data, []byte(`"`+key+`"`)) {
			return true
		}
	}
	return bytes.Contains(data, []byte(`"cache_creation_input_tokens"`)) ||
		bytes.Contains(data, []byte(`"cache_read_input_tokens"`))
}

func isOpenAIFamilyResponse(data string) bool {
	return gjson.Get(data, "choices").IsArray() || gjson.Get(data, "output").IsArray()
}

// sanitizeInternalResponseFields removes every blacklisted key from a JSON
// payload, then removes object ancestors that the removal left empty so no
// empty {} is shipped to the client. Pre-existing empty objects are kept: only
// objects this strip emptied are pruned. The payload is edited with path
// deletions, so untouched bytes (including content text) are preserved verbatim.
func sanitizeInternalResponseFields(data string) (string, bool) {
	if !containsInternalResponseKey([]byte(data)) {
		return data, false
	}
	openAIFamily := isOpenAIFamilyResponse(data)
	changed := false
	for {
		path, found := findInternalKeyPath(gjson.Parse(data), "", openAIFamily)
		if !found {
			break
		}
		updated, err := sjson.Delete(data, path)
		if err != nil || updated == data {
			break
		}
		data = updated
		changed = true
		data = pruneEmptiedAncestors(data, path)
	}
	return data, changed
}

func sanitizeInternalResponseBytes(data []byte) ([]byte, bool) {
	if !containsInternalResponseKey(data) {
		return data, false
	}
	sanitized, changed := sanitizeInternalResponseFields(string(data))
	if !changed {
		return data, false
	}
	return common.StringToByteSlice(sanitized), true
}

func findInternalKeyPath(node gjson.Result, prefix string, openAIFamily bool) (string, bool) {
	switch {
	case node.IsObject():
		var found string
		var hit bool
		node.ForEach(func(key gjson.Result, value gjson.Result) bool {
			name := key.String()
			path := joinObjectSegment(prefix, name)
			if isInternalResponseKey(name, openAIFamily) {
				found, hit = path, true
				return false
			}
			found, hit = findInternalKeyPath(value, path, openAIFamily)
			return !hit
		})
		return found, hit
	case node.IsArray():
		var found string
		var hit bool
		index := 0
		node.ForEach(func(_ gjson.Result, value gjson.Result) bool {
			found, hit = findInternalKeyPath(value, joinIndexSegment(prefix, index), openAIFamily)
			index++
			return !hit
		})
		return found, hit
	}
	return "", false
}

// pruneEmptiedAncestors walks up from a deleted path and removes each parent
// object the deletion left empty, stopping at the first non-empty or array
// parent (arrays are never pruned) and never at the document root.
func pruneEmptiedAncestors(data string, path string) string {
	for parent := parentPath(path); parent != ""; parent = parentPath(parent) {
		node := gjson.Get(data, parent)
		if !node.IsObject() || !isObjectEmpty(node) {
			return data
		}
		updated, err := sjson.Delete(data, parent)
		if err != nil || updated == data {
			return data
		}
		data = updated
	}
	return data
}

func isObjectEmpty(node gjson.Result) bool {
	empty := true
	node.ForEach(func(_ gjson.Result, _ gjson.Result) bool {
		empty = false
		return false
	})
	return empty
}

func joinObjectSegment(prefix, key string) string {
	escaped := escapePathSegment(key)
	if prefix == "" {
		return escaped
	}
	return prefix + "." + escaped
}

func joinIndexSegment(prefix string, index int) string {
	if prefix == "" {
		return strconv.Itoa(index)
	}
	return prefix + "." + strconv.Itoa(index)
}

// escapePathSegment quotes the characters gjson and sjson treat as path syntax
// so an upstream key such as "u.s" or "a#b" addresses itself, not a nested node.
func escapePathSegment(key string) string {
	var builder strings.Builder
	builder.Grow(len(key) + 4)
	for _, r := range key {
		switch r {
		case '.', '#', '*', '?', '@', '[', ']', '\\':
			builder.WriteRune('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// parentPath drops the last unescaped segment of a path built above.
func parentPath(path string) string {
	for index := len(path) - 1; index > 0; index-- {
		if path[index] == '.' && !segmentEscaped(path, index) {
			return path[:index]
		}
	}
	return ""
}

func segmentEscaped(path string, index int) bool {
	backslashes := 0
	for cursor := index - 1; cursor >= 0 && path[cursor] == '\\'; cursor-- {
		backslashes++
	}
	return backslashes%2 == 1
}
