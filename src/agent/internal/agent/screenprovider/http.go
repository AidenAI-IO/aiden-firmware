package screenprovider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTP fetches frames from an environment bridge at /api/providers/screenshot.
type HTTP struct {
	endpoint        string
	benchmarkTaskID string
	httpClient      *http.Client
}

func NewHTTP(endpoint, benchmarkTaskID string) *HTTP {
	return NewHTTPWithClient(endpoint, benchmarkTaskID, &http.Client{Timeout: DefaultTimeout})
}

func NewHTTPWithClient(endpoint, benchmarkTaskID string, client *http.Client) *HTTP {
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &HTTP{
		endpoint:        strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		benchmarkTaskID: strings.TrimSpace(benchmarkTaskID),
		httpClient:      client,
	}
}

type httpRequestBody struct {
	Format       string `json:"format"`
	Quality      int    `json:"quality"`
	CropBlack    bool   `json:"crop_black"`
	MinimalWidth int    `json:"minimal_width"`
}

type httpErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type httpResponseBody struct {
	OK   bool `json:"ok"`
	Data struct {
		Meta        FrameMetadata `json:"meta"`
		CaptureInfo CaptureInfo   `json:"capture_info"`
		Image       string        `json:"image"`
	} `json:"data"`
	Error *httpErrorBody `json:"error,omitempty"`
}

func (c *HTTP) LatestFrameWithFormat(format string, quality int, cropBlack bool, minimalWidth int) (*FrameMetadata, []byte, CaptureInfo, error) {
	if c == nil || c.endpoint == "" {
		return nil, nil, CaptureInfo{}, fmt.Errorf("remote screen provider not configured")
	}
	if format == "" {
		format = "jpeg"
	}
	if quality <= 0 {
		quality = DefaultJPEGQuality
	}
	if minimalWidth < 0 {
		minimalWidth = 0
	}

	bodyBytes, err := json.Marshal(httpRequestBody{
		Format:       format,
		Quality:      quality,
		CropBlack:    cropBlack,
		MinimalWidth: minimalWidth,
	})
	if err != nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("marshal screenshot provider request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.endpoint+Path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("create screenshot provider request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.benchmarkTaskID != "" {
		req.Header.Set(TaskIDHeader, c.benchmarkTaskID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("screenshot provider http call failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("read screenshot provider response: %w", err)
	}

	var parsed httpResponseBody
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, nil, CaptureInfo{}, fmt.Errorf("screenshot provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
		return nil, nil, CaptureInfo{}, fmt.Errorf("parse screenshot provider response: %w", err)
	}
	if !parsed.OK || parsed.Error != nil {
		msg := "screenshot provider failed"
		if parsed.Error != nil {
			if parsed.Error.Message != "" {
				msg = parsed.Error.Message
			} else if parsed.Error.Code != "" {
				msg = parsed.Error.Code
			}
		}
		return nil, nil, CaptureInfo{}, fmt.Errorf("%s", msg)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, CaptureInfo{}, fmt.Errorf("screenshot provider returned HTTP %d", resp.StatusCode)
	}

	imageBytes, err := base64.StdEncoding.DecodeString(parsed.Data.Image)
	if err != nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("decode screenshot provider image: %w", err)
	}
	meta := parsed.Data.Meta
	if meta.Bytes == 0 {
		meta.Bytes = uint64(len(imageBytes))
	}
	if meta.PixelFormat == "" {
		meta.PixelFormat = format
	}
	return &meta, imageBytes, CloneCaptureInfo(parsed.Data.CaptureInfo), nil
}
