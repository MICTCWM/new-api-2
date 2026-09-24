// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type ModelSplitUsageView struct {
	RuleId          string `json:"rule_id"`
	Group           string `json:"group"`
	RemainingTotal  int    `json:"remaining_total"`
	RemainingModel1 int    `json:"remaining_model_1"`
	RemainingModel2 int    `json:"remaining_model_2"`
	Window          int    `json:"window"`
	Model1          string `json:"model_1"`
	Model2          string `json:"model_2"`
}

func ApplyModelSplitRouting(c *gin.Context, originalModel string) (*model.ModelSplitReservation, error) {
	originalModel = strings.TrimSpace(originalModel)
	if c == nil || originalModel == "" {
		return nil, nil
	}
	candidateGroups := ResolveRequestCandidateGroups(c)
	rule := operation_setting.MatchModelSplitRuleForGroups(candidateGroups, originalModel)
	if rule == nil {
		return nil, nil
	}
	// Bookkeeping must use the real matched group so admin views (keyed by
	// rule.Groups) find the row; the literal "auto" would never be listed.
	matchedGroup := ""
	for _, group := range candidateGroups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		for _, ruleGroup := range rule.Groups {
			if strings.TrimSpace(ruleGroup) == group {
				matchedGroup = group
				break
			}
		}
		if matchedGroup != "" {
			break
		}
	}
	if matchedGroup == "" {
		return nil, nil
	}

	forceImageSide := 0
	if rule.ImageSplitEnabled && RequestContainsImage(c) {
		if strings.TrimSpace(rule.ImageModel) == strings.TrimSpace(rule.Model2) {
			forceImageSide = 2
		} else {
			forceImageSide = 1
		}
	}

	reservation, err := model.ReserveModelSplitUsage(strings.TrimSpace(rule.ID), matchedGroup, rule.Percent1, rule.Percent2, forceImageSide)
	if err != nil {
		return nil, err
	}
	if reservation == nil {
		return nil, nil
	}
	reservation.TargetModel = strings.TrimSpace(rule.Model1)
	if reservation.Side == 2 {
		reservation.TargetModel = strings.TrimSpace(rule.Model2)
	}
	common.SetContextKey(c, constant.ContextKeyModelSplitTarget, reservation.TargetModel)
	common.SetContextKey(c, constant.ContextKeyModelSplitReservation, reservation)
	// Split routing only records the target; original_model stays the user request.
	return reservation, nil
}

// ResolveRequestCandidateGroups returns the candidate groups a request may be
// routed to. Auto requests resolve to real groups: prefer an already resolved
// Auto group, otherwise fall back to the ordered Auto candidate list. Split
// routing and response rewriting share this resolution so both agree on which
// group a request actually landed on.
func ResolveRequestCandidateGroups(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	usingGroup := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
	if usingGroup == "auto" {
		if resolved := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyAutoGroup)); resolved != "" {
			return []string{resolved}
		}
		if autoGroups := GetRequestAutoGroups(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup)); len(autoGroups) > 0 {
			return autoGroups
		}
	}
	return []string{usingGroup}
}

func RequestModelSplitTarget(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyModelSplitTarget))
}

func ApplyModelSplitMappingOverride(mappingJSON, originalModel, splitTarget string) string {
	originalModel = strings.TrimSpace(originalModel)
	splitTarget = strings.TrimSpace(splitTarget)
	if originalModel == "" || splitTarget == "" {
		return mappingJSON
	}
	// Splitting back to the original model means "keep this model's own mapping
	// chain" (channel mapping + group mapping). Writing a self-mapping
	// (alias -> alias) would make ModelMappedHelper treat the model as unmapped
	// and discard the existing mapping, so short circuit and keep it untouched.
	if splitTarget == originalModel {
		return mappingJSON
	}
	merged := make(map[string]string)
	trimmed := strings.TrimSpace(mappingJSON)
	if trimmed != "" && trimmed != "{}" {
		if err := common.UnmarshalJsonStr(trimmed, &merged); err != nil {
			merged = make(map[string]string)
		}
	}
	merged[originalModel] = splitTarget
	encoded, err := common.Marshal(merged)
	if err != nil {
		return mappingJSON
	}
	return string(encoded)
}

func ListModelSplitUsageViews() ([]ModelSplitUsageView, error) {
	setting := operation_setting.GetModelSplitSetting()
	if setting == nil {
		return []ModelSplitUsageView{}, nil
	}
	rows, err := model.ListModelSplitUsages()
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]model.ModelSplitUsage, len(rows))
	for _, row := range rows {
		byKey[row.RuleId+"\x00"+row.GroupName] = row
	}

	views := make([]ModelSplitUsageView, 0)
	for _, rule := range setting.Rules {
		// Reservations are recorded with the trimmed rule ID, so the view must
		// use the same key or admin usage would fall back to the full window.
		ruleID := strings.TrimSpace(rule.ID)
		seenGroups := make(map[string]struct{})
		groups := make([]string, 0, len(rule.Groups))
		for _, group := range rule.Groups {
			group = strings.TrimSpace(group)
			if group == "" {
				continue
			}
			if _, ok := seenGroups[group]; ok {
				continue
			}
			seenGroups[group] = struct{}{}
			groups = append(groups, group)
		}
		for _, group := range groups {
			view := ModelSplitUsageView{
				RuleId:          ruleID,
				Group:           group,
				RemainingTotal:  operation_setting.ModelSplitWindowSize,
				RemainingModel1: rule.Percent1,
				RemainingModel2: rule.Percent2,
				Window:          operation_setting.ModelSplitWindowSize,
				Model1:          rule.Model1,
				Model2:          rule.Model2,
			}
			if row, ok := byKey[ruleID+"\x00"+group]; ok {
				view.RemainingTotal = row.RemainingTotal
				view.RemainingModel1 = row.Remaining1
				view.RemainingModel2 = row.Remaining2
			}
			views = append(views, view)
		}
	}
	return views, nil
}

func RequestContainsImage(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := c.Request.URL.Path
	if strings.Contains(path, "/mj/") || strings.Contains(path, "/suno/") ||
		strings.Contains(path, "/v1/videos") || strings.Contains(path, "/v1/video/") {
		return false
	}
	contentType := c.Request.Header.Get("Content-Type")
	// Parse the media type so a multipart boundary that merely contains "json"
	// cannot be mistaken for a JSON body. Fall back to the old substring check
	// when the header is malformed so abnormal requests keep prior behavior.
	isJSON := false
	isMultipart := false
	if mediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		mediaType = strings.ToLower(mediaType)
		isJSON = mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
		isMultipart = mediaType == "multipart/form-data"
	} else {
		lowered := strings.ToLower(contentType)
		isJSON = contentType == "" || strings.Contains(lowered, "json")
		isMultipart = strings.Contains(lowered, "multipart/form-data")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return false
	}
	body, err := storage.Bytes()
	if err != nil {
		return false
	}
	found := false
	if isJSON {
		found = gjson.ValidBytes(body) && jsonBodyContainsImage(body)
	} else if isMultipart {
		found = multipartBodyContainsImage(contentType, body)
	}
	// Restore the original body for downstream relay regardless of the result.
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
		c.Request.Body = io.NopCloser(storage)
	}
	return found
}

// multipartBodyContainsImage reports whether a multipart/form-data body (for
// example /v1/images/edits) carries an uploaded file field named with "image".
func multipartBodyContainsImage(contentType string, body []byte) bool {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || strings.ToLower(mediaType) != "multipart/form-data" {
		return false
	}
	boundary := params["boundary"]
	if boundary == "" {
		return false
	}
	reader := multipart.NewReader(bufio.NewReader(bytes.NewReader(body)), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return false
		}
		if part.FileName() != "" && strings.Contains(strings.ToLower(part.FormName()), "image") {
			return true
		}
	}
}

func jsonBodyContainsImage(body []byte) bool {
	if chatMessagesContainImage(gjson.GetBytes(body, "messages")) {
		return true
	}
	if responsesInputContainsImage(gjson.GetBytes(body, "input")) {
		return true
	}
	if claudeContentContainsImage(gjson.GetBytes(body, "content")) {
		return true
	}
	if claudeContentContainsImage(gjson.GetBytes(body, "system")) {
		return true
	}
	if geminiContentsContainImage(gjson.GetBytes(body, "contents")) {
		return true
	}
	return false
}

func chatMessagesContainImage(messages gjson.Result) bool {
	if !messages.IsArray() {
		return false
	}
	found := false
	messages.ForEach(func(_, message gjson.Result) bool {
		content := message.Get("content")
		if contentHasTypedPart(content, "image_url") || contentHasTypedPart(content, "image") {
			found = true
			return false
		}
		return true
	})
	return found
}

func responsesInputContainsImage(input gjson.Result) bool {
	if input.Get("type").String() == "input_image" {
		return true
	}
	if contentHasTypedPart(input.Get("content"), "input_image") {
		return true
	}
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "input_image" || contentHasTypedPart(item.Get("content"), "input_image") {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeContentContainsImage(content gjson.Result) bool {
	return contentHasTypedPart(content, "image")
}

func contentHasTypedPart(content gjson.Result, wantType string) bool {
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == wantType {
			found = true
			return false
		}
		return true
	})
	return found
}

func geminiContentsContainImage(contents gjson.Result) bool {
	if !contents.IsArray() {
		return false
	}
	found := false
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			if geminiPartIsImage(part) {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

func geminiPartIsImage(part gjson.Result) bool {
	inline := part.Get("inlineData")
	if !inline.Exists() {
		inline = part.Get("inline_data")
	}
	if inline.Exists() {
		mimeType := inline.Get("mimeType").String()
		if mimeType == "" {
			mimeType = inline.Get("mime_type").String()
		}
		if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			return true
		}
	}

	fileData := part.Get("fileData")
	if !fileData.Exists() {
		fileData = part.Get("file_data")
	}
	if !fileData.Exists() {
		return false
	}
	mimeType := strings.ToLower(strings.TrimSpace(fileData.Get("mimeType").String()))
	if mimeType == "" {
		mimeType = strings.ToLower(strings.TrimSpace(fileData.Get("mime_type").String()))
	}
	if strings.HasPrefix(mimeType, "image/") {
		return true
	}
	if mimeType != "" {
		return false
	}
	uri := strings.TrimSpace(fileData.Get("fileUri").String())
	if uri == "" {
		uri = strings.TrimSpace(fileData.Get("file_uri").String())
	}
	return uri != ""
}
