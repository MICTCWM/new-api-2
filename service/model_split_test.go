// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useModelSplitTestDB swaps model.DB for an in-memory SQLite database so the
// reservation bookkeeping under test is isolated per test.
func useModelSplitTestDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ModelSplitUsage{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func withModelSplitSetting(t *testing.T, enabled bool, rules []operation_setting.ModelSplitRule) {
	t.Helper()
	setting := operation_setting.GetModelSplitSetting()
	originalEnabled := setting.Enabled
	originalRules := setting.Rules
	setting.Enabled = enabled
	setting.Rules = rules
	t.Cleanup(func() {
		setting.Enabled = originalEnabled
		setting.Rules = originalRules
	})
}

func ginJSONContext(t *testing.T, path, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c
}

func TestJSONBodyContainsImageFormats(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "chat image_url object",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]}]}`,
			want: true,
		},
		{
			name: "chat image_url string",
			body: `{"messages":[{"content":[{"type":"image_url","image_url":"https://x/a.png"}]}]}`,
			want: true,
		},
		{
			name: "responses input_image",
			body: `{"input":[{"type":"input_image","image_url":"https://x/a.png"}]}`,
			want: true,
		},
		{
			name: "responses message content input_image",
			body: `{"input":[{"type":"message","content":[{"type":"input_image","image_url":"https://x/a.png"}]}]}`,
			want: true,
		},
		{
			name: "claude image",
			body: `{"content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64"}}]}`,
			want: true,
		},
		{
			name: "gemini inlineData",
			body: `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]}]}`,
			want: true,
		},
		{
			name: "gemini inline_data",
			body: `{"contents":[{"parts":[{"inline_data":{"mime_type":"image/jpeg","data":"abc"}}]}]}`,
			want: true,
		},
		{
			name: "gemini fileData image mime",
			body: `{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"https://x/a.png"}}]}]}`,
			want: true,
		},
		{
			name: "gemini file_data image mime_type",
			body: `{"contents":[{"parts":[{"file_data":{"mime_type":"image/jpeg","file_uri":"https://x/a.jpg"}}]}]}`,
			want: true,
		},
		{
			name: "gemini fileData empty mime with fileUri",
			body: `{"contents":[{"parts":[{"fileData":{"fileUri":"https://generativelanguage.googleapis.com/files/abc"}}]}]}`,
			want: true,
		},
		{
			name: "gemini file_data empty mime with file_uri",
			body: `{"contents":[{"parts":[{"file_data":{"file_uri":"https://generativelanguage.googleapis.com/files/abc"}}]}]}`,
			want: true,
		},
		{
			name: "plain text chat",
			body: `{"messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			name: "gemini non-image inline data",
			body: `{"contents":[{"parts":[{"inlineData":{"mimeType":"application/pdf","data":"abc"}}]}]}`,
			want: false,
		},
		{
			name: "gemini fileData non-image mime",
			body: `{"contents":[{"parts":[{"fileData":{"mimeType":"application/pdf","fileUri":"https://x/a.pdf"}}]}]}`,
			want: false,
		},
		{
			name: "gemini fileData empty mime and empty uri",
			body: `{"contents":[{"parts":[{"fileData":{"mimeType":"","fileUri":""}}]}]}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, jsonBodyContainsImage([]byte(tt.body)))
		})
	}
}

func TestRequestContainsImageSkipsTaskPaths(t *testing.T) {
	c := ginJSONContext(t, "/mj/submit/imagine", `{"content":[{"type":"image"}]}`)
	require.False(t, RequestContainsImage(c))

	c = ginJSONContext(t, "/suno/submit/music", `{"messages":[{"content":[{"type":"image_url","image_url":"https://x"}]}]}`)
	require.False(t, RequestContainsImage(c))

	c = ginJSONContext(t, "/v1/videos", `{"messages":[{"content":[{"type":"image_url","image_url":"https://x"}]}]}`)
	require.False(t, RequestContainsImage(c))

	c = ginJSONContext(t, "/v1/chat/completions", `{"messages":[{"content":[{"type":"image_url","image_url":"https://x"}]}]}`)
	require.True(t, RequestContainsImage(c))
}

func multipartRequestContext(t *testing.T, path, fieldName, fileName, fileContent string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile(fieldName, fileName)
	require.NoError(t, err)
	_, err = part.Write([]byte(fileContent))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("prompt", "keep-me"))
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.Request = req
	return c
}

func TestRequestContainsImageMultipartImageFields(t *testing.T) {
	c := multipartRequestContext(t, "/v1/images/edits", "image", "a.png", "PNGDATA")
	require.True(t, RequestContainsImage(c))
	// The body must stay readable for the downstream relay after detection.
	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "PNGDATA")
	require.Contains(t, string(body), "keep-me")

	c = multipartRequestContext(t, "/v1/images/edits", "image[]", "a.png", "PNGDATA")
	require.True(t, RequestContainsImage(c))

	c = multipartRequestContext(t, "/v1/images/edits", "image_0", "a.png", "PNGDATA")
	require.True(t, RequestContainsImage(c))
}

func TestRequestContainsImageMultipartAudioFileField(t *testing.T) {
	c := multipartRequestContext(t, "/v1/audio/transcriptions", "file", "a.mp3", "AUDIODATA")
	require.False(t, RequestContainsImage(c))
	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "AUDIODATA")
}

func TestRequestContainsImageMultipartTaskPathSkipped(t *testing.T) {
	c := multipartRequestContext(t, "/v1/videos", "image", "a.png", "PNGDATA")
	require.False(t, RequestContainsImage(c))
}

// multipartJSONBoundaryContext builds a multipart request whose boundary itself
// contains the substring "json", which used to be mistaken for a JSON body.
func multipartJSONBoundaryContext(t *testing.T, path, fieldName, fileName, fileContent string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.SetBoundary("----jsonBoundaryXYZ"))
	part, err := writer.CreateFormFile(fieldName, fileName)
	require.NoError(t, err)
	_, err = part.Write([]byte(fileContent))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.Request = req
	return c
}

func TestRequestContainsImageMultipartBoundaryContainingJSON(t *testing.T) {
	c := multipartJSONBoundaryContext(t, "/v1/images/edits", "image", "a.png", "PNGDATA")
	require.Contains(t, c.Request.Header.Get("Content-Type"), "json")
	require.True(t, RequestContainsImage(c))

	audioCtx := multipartJSONBoundaryContext(t, "/v1/audio/transcriptions", "file", "a.mp3", "AUDIODATA")
	require.False(t, RequestContainsImage(audioCtx))
}

func TestApplyModelSplitMappingOverrideCoversChannelMapping(t *testing.T) {
	merged := ApplyModelSplitMappingOverride(`{"nn":"v4.1","other":"keep"}`, "nn", "split-target")
	var parsed map[string]string
	require.NoError(t, common.Unmarshal([]byte(merged), &parsed))
	require.Equal(t, "split-target", parsed["nn"])
	require.Equal(t, "keep", parsed["other"])

	// A non-self target must not disturb unrelated keys either.
	merged = ApplyModelSplitMappingOverride(`{"alias":"native-a","other":"keep"}`, "alias", "native-b")
	parsed = make(map[string]string)
	require.NoError(t, common.Unmarshal([]byte(merged), &parsed))
	require.Equal(t, "native-b", parsed["alias"])
	require.Equal(t, "keep", parsed["other"])
	require.Len(t, parsed, 2)
}

// TestApplyModelSplitMappingOverrideSelfTargetKeepsChain verifies that splitting
// to the original model keeps the provided mapping untouched: writing an
// alias -> alias self-mapping would make ModelMappedHelper drop the channel
// mapping and reject the request on whitelist channels.
func TestApplyModelSplitMappingOverrideSelfTargetKeepsChain(t *testing.T) {
	tests := []struct {
		name    string
		mapping string
	}{
		{name: "channel mapping kept", mapping: `{"alias":"native-model"}`},
		{name: "empty string kept", mapping: ``},
		{name: "empty object kept", mapping: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyModelSplitMappingOverride(tt.mapping, "alias", "alias")
			require.Equal(t, tt.mapping, got)
		})
	}

	// Both sides are compared after trimming, so a padded self-target behaves
	// exactly like an exact self-target.
	require.Equal(t, `{"alias":"native-model"}`,
		ApplyModelSplitMappingOverride(`{"alias":"native-model"}`, " alias ", " alias "))
}

func TestApplyModelSplitRoutingDisabledSwitch(t *testing.T) {
	withModelSplitSetting(t, false, []operation_setting.ModelSplitRule{{
		ID:            "r1",
		Enabled:       true,
		Groups:        []string{"vip"},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})
	c := ginJSONContext(t, "/v1/chat/completions", `{"model":"nn","messages":[{"role":"user","content":"hi"}]}`)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
	reservation, err := ApplyModelSplitRouting(c, "nn")
	require.NoError(t, err)
	require.Nil(t, reservation)
	require.Empty(t, RequestModelSplitTarget(c))
}

func TestApplyModelSplitRoutingReservesAndSetsTarget(t *testing.T) {
	useModelSplitTestDB(t)

	withModelSplitSetting(t, true, []operation_setting.ModelSplitRule{{
		ID:            "r1",
		Enabled:       true,
		Groups:        []string{"vip"},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})
	c := ginJSONContext(t, "/v1/chat/completions", `{"model":"nn","messages":[{"role":"user","content":"hi"}]}`)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
	reservation, err := ApplyModelSplitRouting(c, "nn")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.Equal(t, "model-a", reservation.TargetModel)
	require.Equal(t, "model-a", RequestModelSplitTarget(c))

	param := &RetryParam{Ctx: c, ModelName: "nn"}
	require.Equal(t, "model-a", channelLookupModel(param, "vip"))
	copied := retryParamForGroup(param, "vip")
	require.Equal(t, "model-a", copied.ModelName)
}

func TestApplyModelSplitRoutingUsesResolvedAutoGroup(t *testing.T) {
	useModelSplitTestDB(t)

	withModelSplitSetting(t, true, []operation_setting.ModelSplitRule{{
		ID:            "r1",
		Enabled:       true,
		Groups:        []string{"vip"},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})

	c := ginJSONContext(t, "/v1/chat/completions", `{"model":"nn","messages":[{"role":"user","content":"hi"}]}`)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(c, constant.ContextKeyAutoGroup, "vip")
	reservation, err := ApplyModelSplitRouting(c, "nn")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.Equal(t, "vip", reservation.GroupName)

	// Bookkeeping is recorded under the real group, not the literal "auto".
	usage, err := model.GetModelSplitUsage("r1", "vip")
	require.NoError(t, err)
	require.NotNil(t, usage)
	autoUsage, err := model.GetModelSplitUsage("r1", "auto")
	require.NoError(t, err)
	require.Nil(t, autoUsage)

	// A resolved Auto group outside the rule's groups does not match.
	missCtx := ginJSONContext(t, "/v1/chat/completions", `{"model":"nn","messages":[{"role":"user","content":"hi"}]}`)
	common.SetContextKey(missCtx, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(missCtx, constant.ContextKeyAutoGroup, "not-configured")
	missReservation, err := ApplyModelSplitRouting(missCtx, "nn")
	require.NoError(t, err)
	require.Nil(t, missReservation)
}

func TestListModelSplitUsageViewsReturnsFullWindowWithoutRow(t *testing.T) {
	useModelSplitTestDB(t)

	withModelSplitSetting(t, true, []operation_setting.ModelSplitRule{{
		ID:            "r1",
		Enabled:       true,
		Groups:        []string{"vip"},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})
	views, err := ListModelSplitUsageViews()
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, 100, views[0].Window)
	require.Equal(t, 100, views[0].RemainingTotal)
	require.Equal(t, 57, views[0].RemainingModel1)
	require.Equal(t, 43, views[0].RemainingModel2)
	require.Equal(t, "vip", views[0].Group)
}

func TestListModelSplitUsageViewsTrimsRuleID(t *testing.T) {
	useModelSplitTestDB(t)

	withModelSplitSetting(t, true, []operation_setting.ModelSplitRule{{
		ID:            " r1 ",
		Enabled:       true,
		Groups:        []string{" vip "},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})

	// Reservations store the trimmed rule ID, so the view must look it up the
	// same way instead of showing a full window for a spaced-out configured ID.
	_, err := model.ReserveModelSplitUsage("r1", "vip", 57, 43, 0)
	require.NoError(t, err)

	views, err := ListModelSplitUsageViews()
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, "r1", views[0].RuleId)
	require.Equal(t, "vip", views[0].Group)
	require.Equal(t, 99, views[0].RemainingTotal)
	require.Equal(t, 56, views[0].RemainingModel1)
	require.Equal(t, 43, views[0].RemainingModel2)
}

func TestApplyModelSplitRoutingAutoWithoutResolvedAutoGroup(t *testing.T) {
	useModelSplitTestDB(t)

	// Auto candidates are initialized the same way the request path reads them:
	// GetUserAutoGroup -> auto list filtered by usable group + group ratio.
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})

	withModelSplitSetting(t, true, []operation_setting.ModelSplitRule{{
		ID:            "r1",
		Enabled:       true,
		Groups:        []string{"vip"},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})

	// Distribution runs before Auto resolves into ContextKeyAutoGroup, so the
	// routing must fall back to the user's ordered Auto candidates and match the
	// rule on the second candidate.
	c := ginJSONContext(t, "/v1/chat/completions", `{"model":"nn","messages":[{"role":"user","content":"hi"}]}`)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	require.Empty(t, common.GetContextKeyString(c, constant.ContextKeyAutoGroup))
	reservation, err := ApplyModelSplitRouting(c, "nn")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.Equal(t, "vip", reservation.GroupName)
	require.Equal(t, "model-a", reservation.TargetModel)
	require.Equal(t, "model-a", RequestModelSplitTarget(c))

	// The usage is booked under the real matched candidate group.
	vipUsage, err := model.GetModelSplitUsage("r1", "vip")
	require.NoError(t, err)
	require.NotNil(t, vipUsage)
	require.Equal(t, 56, vipUsage.Remaining1)
	require.Equal(t, 43, vipUsage.Remaining2)
	require.Equal(t, 99, vipUsage.RemainingTotal)

	// When no candidate hits the rule there is no split and no bookkeeping.
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default"]`))
	missCtx := ginJSONContext(t, "/v1/chat/completions", `{"model":"nn","messages":[{"role":"user","content":"hi"}]}`)
	common.SetContextKey(missCtx, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(missCtx, constant.ContextKeyUserGroup, "default")
	missReservation, err := ApplyModelSplitRouting(missCtx, "nn")
	require.NoError(t, err)
	require.Nil(t, missReservation)
	require.Empty(t, RequestModelSplitTarget(missCtx))

	for _, group := range []string{"auto", "default"} {
		usage, err := model.GetModelSplitUsage("r1", group)
		require.NoError(t, err)
		require.Nil(t, usage)
	}
}

func TestRequestContainsImageCaseInsensitiveContentType(t *testing.T) {
	c := ginJSONContext(t, "/v1/chat/completions", `{"messages":[{"content":[{"type":"image_url","image_url":"https://x/a.png"}]}]}`)
	c.Request.Header.Set("Content-Type", "application/JSON; charset=UTF-8")
	require.True(t, RequestContainsImage(c))

	multipartCtx := multipartRequestContext(t, "/v1/images/edits", "image", "a.png", "PNGDATA")
	parts := strings.SplitN(multipartCtx.Request.Header.Get("Content-Type"), ";", 2)
	require.Len(t, parts, 2)
	multipartCtx.Request.Header.Set("Content-Type", strings.ToUpper(parts[0])+";"+parts[1])
	require.True(t, RequestContainsImage(multipartCtx))
}
