package channel

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

// preparedAPIRequestBody describes the first upstream attempt and, when a
// compressed JSON body was chosen, how to replay the original body once.
type preparedAPIRequestBody struct {
	first          io.Reader
	firstSize      int64
	firstEncoding  string
	compressed     bool
	originalSize   int64
	compressedSize int64
	original       func() (io.Reader, int64, error)
}

func prepareAPIRequestBody(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*preparedAPIRequestBody, error) {
	originalSize, sizeKnown := requestBodySize(body)
	if body == nil || info == nil || info.ChannelMeta == nil || !info.ChannelSetting.GzipEnabled || !isJSONAPIRequest(c) {
		return &preparedAPIRequestBody{
			first:     body,
			firstSize: sizeOrUnknown(originalSize, sizeKnown),
		}, nil
	}

	if replayable, ok := body.(common.ReplayableBody); ok {
		size := replayable.Size()
		if size <= 0 {
			return &preparedAPIRequestBody{
				first:     body,
				firstSize: size,
			}, nil
		}
		compressedBytes, err := compressReplayableBody(replayable)
		if err != nil {
			// Compression is best-effort. A local gzip failure must not turn a
			// valid relay request into a failed request.
			return &preparedAPIRequestBody{
				first:     body,
				firstSize: size,
			}, nil
		}
		// Compression is optional; never send a larger payload just because the
		// channel has gzip enabled.
		if int64(len(compressedBytes)) >= size {
			return &preparedAPIRequestBody{
				first:     body,
				firstSize: size,
			}, nil
		}
		return &preparedAPIRequestBody{
			first:          bytes.NewReader(compressedBytes),
			firstSize:      int64(len(compressedBytes)),
			firstEncoding:  "gzip",
			compressed:     true,
			originalSize:   size,
			compressedSize: int64(len(compressedBytes)),
			original: func() (io.Reader, int64, error) {
				reader, err := replayable.NewReader()
				if err != nil {
					return nil, 0, err
				}
				return reader, size, nil
			},
		}, nil
	}

	// Non-replayable bodies are uncommon on the JSON relay path, but reading
	// them once keeps the compression/fallback contract identical.
	originalBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	compressedBytes, err := compressBytes(originalBytes)
	if err != nil {
		return &preparedAPIRequestBody{
			first:     bytes.NewReader(originalBytes),
			firstSize: int64(len(originalBytes)),
		}, nil
	}
	if len(compressedBytes) >= len(originalBytes) {
		return &preparedAPIRequestBody{
			first:     bytes.NewReader(originalBytes),
			firstSize: int64(len(originalBytes)),
		}, nil
	}
	return &preparedAPIRequestBody{
		first:          bytes.NewReader(compressedBytes),
		firstSize:      int64(len(compressedBytes)),
		firstEncoding:  "gzip",
		compressed:     true,
		originalSize:   int64(len(originalBytes)),
		compressedSize: int64(len(compressedBytes)),
		original: func() (io.Reader, int64, error) {
			return bytes.NewReader(originalBytes), int64(len(originalBytes)), nil
		},
	}, nil
}

func requestBodySize(body io.Reader) (int64, bool) {
	if body == nil {
		return 0, false
	}
	if replayable, ok := body.(common.ReplayableBody); ok {
		return replayable.Size(), true
	}
	switch reader := body.(type) {
	case *bytes.Reader:
		return int64(reader.Len()), true
	case *bytes.Buffer:
		return int64(reader.Len()), true
	case *strings.Reader:
		return int64(reader.Len()), true
	default:
		return 0, false
	}
}

func sizeOrUnknown(size int64, known bool) int64 {
	if known {
		return size
	}
	return -1
}

func compressReplayableBody(body common.ReplayableBody) ([]byte, error) {
	reader, err := body.NewReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return compressReader(reader)
}

func compressReader(reader io.Reader) ([]byte, error) {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(writer, reader); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func compressBytes(data []byte) ([]byte, error) {
	return compressReader(bytes.NewReader(data))
}

func isJSONAPIRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(c.Request.Header.Get("Content-Type")))
	if separator := strings.IndexByte(contentType, ';'); separator >= 0 {
		contentType = strings.TrimSpace(contentType[:separator])
	}
	return contentType == "application/json" || strings.HasSuffix(contentType, "+json")
}

// shouldRetryGzipRequest decides whether the compressed attempt may be replayed
// with the original JSON. Only errors before a successful upstream response are
// eligible; once a 2xx body is handed back to the relay, retrying would risk
// duplicate output and duplicate billing.
func shouldRetryGzipRequest(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, requestErr error) bool {
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return false
	}
	if info != nil && info.UpstreamContext != nil && info.UpstreamContext.Err() != nil {
		return false
	}
	if requestErr != nil {
		return !errors.Is(requestErr, context.Canceled)
	}
	if resp == nil {
		return false
	}
	return resp.StatusCode < 200 || resp.StatusCode >= 300
}
