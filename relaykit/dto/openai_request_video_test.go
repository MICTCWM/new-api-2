package dto

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesVideoInputIsParsedAndCountedAsVideoMedia(t *testing.T) {
	request := &OpenAIResponsesRequest{
		Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_video","video_url":{"url":"https://example.test/video.mp4"}}]}]`),
	}

	inputs := request.ParseInput()
	require.Len(t, inputs, 2)
	assert.Equal(t, "input_video", inputs[1].Type)
	assert.Equal(t, "https://example.test/video.mp4", inputs[1].VideoUrl)

	meta := request.GetTokenCountMeta()
	require.Len(t, meta.Files, 1)
	assert.Equal(t, types.FileTypeVideo, meta.Files[0].FileType)
	assert.Equal(t, "https://example.test/video.mp4", meta.Files[0].GetRawData())
}
