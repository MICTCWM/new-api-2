package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const (
	responsesVideoFrameLimit = 60
)

var errResponsesVideoTooLong = errors.New("video is too long: maximum 60 frames at one frame every 3 seconds")

// ExpandResponsesVideoFrames replaces Responses input_video parts with JPEG
// input_image parts. It intentionally runs before protocol conversion so every
// existing upstream adaptor receives the same normalized image input.
func ExpandResponsesVideoFrames(c *gin.Context, request *dto.OpenAIResponsesRequest) (bool, error) {
	if request == nil || len(request.Input) == 0 {
		return false, nil
	}

	var input any
	if err := common.Unmarshal(request.Input, &input); err != nil {
		return false, fmt.Errorf("invalid responses input: %w", err)
	}
	changed, expanded, err := expandResponsesVideoValue(c, input)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	encoded, err := common.Marshal(expanded)
	if err != nil {
		return false, fmt.Errorf("marshal expanded responses input: %w", err)
	}
	request.Input = encoded
	return true, nil
}

// ExpandChatVideoFrames replaces Chat Completions video content parts with
// standard image_url parts so chat-only upstreams can process the same video
// input as Responses requests.
func ExpandChatVideoFrames(c *gin.Context, request *dto.GeneralOpenAIRequest) (bool, error) {
	if request == nil || len(request.Messages) == 0 {
		return false, nil
	}

	changed := false
	for i := range request.Messages {
		itemChanged, expanded, err := expandChatVideoValue(c, request.Messages[i].Content)
		if err != nil {
			return false, err
		}
		if itemChanged {
			request.Messages[i].Content = expanded
			changed = true
		}
	}
	return changed, nil
}

func expandChatVideoValue(c *gin.Context, value any) (bool, any, error) {
	switch typed := value.(type) {
	case []any:
		changed := false
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			itemChanged, expanded, err := expandChatVideoValue(c, item)
			if err != nil {
				return false, nil, err
			}
			changed = changed || itemChanged
			if expandedItems, ok := expanded.([]any); ok && itemChanged {
				result = append(result, expandedItems...)
			} else {
				result = append(result, expanded)
			}
		}
		return changed, result, nil
	case map[string]any:
		if isResponsesVideoPart(typed) {
			frames, err := extractChatVideoFrames(c, typed)
			if err != nil {
				return false, nil, err
			}
			return true, frames, nil
		}

		changed := false
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			itemChanged, expanded, err := expandChatVideoValue(c, item)
			if err != nil {
				return false, nil, err
			}
			changed = changed || itemChanged
			result[key] = expanded
		}
		return changed, result, nil
	default:
		return false, value, nil
	}
}

func extractChatVideoFrames(c *gin.Context, part map[string]any) ([]any, error) {
	frames, err := extractResponsesVideoFrames(c, part)
	if err != nil {
		return nil, err
	}

	result := make([]any, 0, len(frames))
	for _, frame := range frames {
		frameMap, ok := frame.(map[string]any)
		if !ok {
			return nil, errors.New("invalid extracted video frame")
		}
		imageURL, ok := frameMap["image_url"].(string)
		if !ok || imageURL == "" {
			return nil, errors.New("extracted video frame has no image data")
		}
		result = append(result, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url":    imageURL,
				"detail": "auto",
			},
		})
	}
	return result, nil
}

func expandResponsesVideoValue(c *gin.Context, value any) (bool, any, error) {
	switch typed := value.(type) {
	case []any:
		changed := false
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			itemChanged, expanded, err := expandResponsesVideoValue(c, item)
			if err != nil {
				return false, nil, err
			}
			changed = changed || itemChanged
			if expandedItems, ok := expanded.([]any); ok && itemChanged {
				result = append(result, expandedItems...)
			} else {
				result = append(result, expanded)
			}
		}
		return changed, result, nil
	case map[string]any:
		if isResponsesVideoPart(typed) {
			frames, err := extractResponsesVideoFrames(c, typed)
			if err != nil {
				return false, nil, err
			}
			return true, frames, nil
		}

		changed := false
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			itemChanged, expanded, err := expandResponsesVideoValue(c, item)
			if err != nil {
				return false, nil, err
			}
			changed = changed || itemChanged
			result[key] = expanded
		}
		return changed, result, nil
	default:
		return false, value, nil
	}
}

func extractResponsesVideoFrames(c *gin.Context, part map[string]any) ([]any, error) {
	videoData := responsesVideoData(part)
	if videoData == "" {
		return nil, errors.New("input_video requires video_url, url, or data")
	}

	workDir, err := os.MkdirTemp("", "new-api-video-frames-")
	if err != nil {
		return nil, fmt.Errorf("create video frame workspace: %w", err)
	}
	defer os.RemoveAll(workDir)

	videoPath := filepath.Join(workDir, "input-video")
	if err := writeResponsesVideoData(c, videoPath, videoData); err != nil {
		return nil, err
	}

	frameRates := []string{"5", "3", "2", "1", "1/2", "1/3"}
	var frames []string
	for attempt, frameRate := range frameRates {
		if attempt > 0 {
			if err := removeFrameFiles(workDir); err != nil {
				return nil, err
			}
		}
		frames, err = extractResponsesVideoFPS(c, videoPath, workDir, attempt, frameRate)
		if err != nil {
			return nil, err
		}
		if len(frames) <= responsesVideoFrameLimit {
			break
		}
	}
	if len(frames) > responsesVideoFrameLimit {
		return nil, errResponsesVideoTooLong
	}
	if len(frames) == 0 {
		return nil, errors.New("video contains no decodable frames")
	}

	result := make([]any, 0, len(frames))
	for _, framePath := range frames {
		data, err := os.ReadFile(framePath)
		if err != nil {
			return nil, fmt.Errorf("read extracted video frame: %w", err)
		}
		result = append(result, map[string]any{
			"type":      "input_image",
			"image_url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
			"detail":    "auto",
		})
	}
	return result, nil
}

func responsesVideoData(part map[string]any) string {
	for _, key := range []string{"video_url", "url", "file_url", "file_data", "data", "file"} {
		if data := nestedVideoString(part[key]); data != "" {
			return data
		}
	}
	return ""
}

func isResponsesVideoPart(part map[string]any) bool {
	typeName := strings.ToLower(strings.TrimSpace(stringValue(part["type"])))
	switch typeName {
	case "input_video", "video", "video_url":
		return true
	case "input_file":
		mimeType := strings.ToLower(strings.TrimSpace(stringValue(part["mime_type"])))
		if strings.HasPrefix(mimeType, "video/") {
			return true
		}
		for _, key := range []string{"file_url", "file_data", "url"} {
			if isVideoURL(nestedVideoString(part[key])) {
				return true
			}
		}
		if file, ok := part["file"].(map[string]any); ok {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(stringValue(file["mime_type"]))), "video/") {
				return true
			}
			if isVideoURL(nestedVideoString(file["file_url"])) || isVideoURL(nestedVideoString(file["file_data"])) {
				return true
			}
		}
	}
	return false
}

func isVideoURL(value string) bool {
	if value == "" || strings.HasPrefix(value, "data:video/") {
		return strings.HasPrefix(value, "data:video/")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	for _, extension := range []string{".mp4", ".webm", ".mov", ".m4v", ".avi", ".mkv", ".mpeg", ".mpg", ".wmv", ".flv"} {
		if strings.HasSuffix(path, extension) {
			return true
		}
	}
	return false
}

func nestedVideoString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"url", "file_url", "file_data", "data"} {
			if nested := nestedVideoString(typed[key]); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func writeResponsesVideoData(c *gin.Context, path string, data string) error {
	maxBytes := responsesVideoMaxBytes()
	if strings.HasPrefix(data, "data:") {
		comma := strings.IndexByte(data, ',')
		if comma < 0 {
			return errors.New("invalid video data URL")
		}
		if len(data)-comma-1 > int(maxBytes/3*4+4) {
			return fmt.Errorf("video exceeds maximum file size of %d MB", maxBytes/(1024*1024))
		}
		decoded, err := base64.StdEncoding.DecodeString(data[comma+1:])
		if err != nil {
			return fmt.Errorf("decode video data URL: %w", err)
		}
		if int64(len(decoded)) > maxBytes {
			return fmt.Errorf("video exceeds maximum file size of %d MB", maxBytes/(1024*1024))
		}
		if err := os.WriteFile(path, decoded, 0600); err != nil {
			return fmt.Errorf("write video data: %w", err)
		}
		return nil
	}
	if !strings.HasPrefix(data, "http://") && !strings.HasPrefix(data, "https://") {
		if len(data) > int(maxBytes/3*4+4) {
			return fmt.Errorf("video exceeds maximum file size of %d MB", maxBytes/(1024*1024))
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return fmt.Errorf("video source must be an HTTP(S) URL or base64 data: %w", err)
		}
		if err := os.WriteFile(path, decoded, 0600); err != nil {
			return fmt.Errorf("write video data: %w", err)
		}
		return nil
	}

	response, err := DoDownloadRequest(data, "extracting Responses video frames")
	if err != nil {
		return fmt.Errorf("download video: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download video: status code %d", response.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create downloaded video: %w", err)
	}
	_, copyErr := io.Copy(file, io.LimitReader(response.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download video body: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close downloaded video: %w", closeErr)
	}
	if stat, statErr := os.Stat(path); statErr != nil {
		return fmt.Errorf("stat downloaded video: %w", statErr)
	} else if stat.Size() > maxBytes {
		return fmt.Errorf("video exceeds maximum file size of %d MB", maxBytes/(1024*1024))
	}
	return nil
}

func responsesVideoMaxBytes() int64 {
	maxBytes := int64(constant.MaxFileDownloadMB) * 1024 * 1024
	if maxBytes <= 0 {
		return 100 * 1024 * 1024
	}
	return maxBytes
}

func extractResponsesVideoFPS(c *gin.Context, videoPath, workDir string, attempt int, fps string) ([]string, error) {
	outputPattern := filepath.Join(workDir, fmt.Sprintf("frame-%02d-%%06d.jpg", attempt))
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	filter := fmt.Sprintf("fps=%s,scale=1280:1280:force_original_aspect_ratio=decrease", fps)
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", videoPath, "-vf", filter, "-frames:v", fmt.Sprint(responsesVideoFrameLimit+1), "-q:v", "5", outputPattern)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("extract video frames with ffmpeg: %s", message)
	}
	files, err := filepath.Glob(filepath.Join(workDir, fmt.Sprintf("frame-%02d-*.jpg", attempt)))
	if err != nil {
		return nil, fmt.Errorf("list extracted video frames: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func removeFrameFiles(workDir string) error {
	files, err := filepath.Glob(filepath.Join(workDir, "frame-*.jpg"))
	if err != nil {
		return fmt.Errorf("list video frames for cleanup: %w", err)
	}
	for _, file := range files {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove extracted video frame: %w", err)
		}
	}
	return nil
}

func stringValue(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}
