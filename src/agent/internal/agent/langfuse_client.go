package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type langfuseClient struct {
	baseURL    string
	publicKey  string
	secretKey  string
	httpClient *http.Client
}

func newLangfuseClient(cfg TelemetryConfig) *langfuseClient {
	return &langfuseClient{
		baseURL:   strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		publicKey: strings.TrimSpace(cfg.PublicKey),
		secretKey: strings.TrimSpace(cfg.SecretKey),
		httpClient: &http.Client{
			Timeout: cfg.UploadTimeoutOrDefault(),
		},
	}
}

func (c *langfuseClient) configured() bool {
	return c != nil && c.baseURL != "" && c.publicKey != "" && c.secretKey != ""
}

type langfuseIngestionRequest struct {
	Batch []langfuseIngestionEvent `json:"batch"`
}

type langfuseIngestionEvent struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Body      json.RawMessage `json:"body"`
}

type langfuseIngestionResponse struct {
	Successes []struct {
		ID     string `json:"id"`
		Status int    `json:"status"`
	} `json:"successes"`
	Errors []struct {
		ID      string `json:"id"`
		Status  int    `json:"status"`
		Message string `json:"message"`
		Error   string `json:"error"`
	} `json:"errors"`
}

func (c *langfuseClient) ingest(ctx context.Context, batch []langfuseIngestionEvent) error {
	if len(batch) == 0 {
		return nil
	}
	if !c.configured() {
		return fmt.Errorf("langfuse client is not configured")
	}
	payload, err := json.Marshal(langfuseIngestionRequest{Batch: batch})
	if err != nil {
		return fmt.Errorf("marshal ingestion batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/public/ingestion", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create ingestion request: %w", err)
	}
	req.SetBasicAuth(c.publicKey, c.secretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ingestion request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ingestion HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed langfuseIngestionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode ingestion response: %w", err)
	}
	if len(parsed.Errors) > 0 {
		parts := make([]string, 0, len(parsed.Errors))
		for _, item := range parsed.Errors {
			msg := strings.TrimSpace(item.Message)
			if msg == "" {
				msg = strings.TrimSpace(item.Error)
			}
			if msg == "" {
				msg = "unknown error"
			}
			parts = append(parts, fmt.Sprintf("%s: %s", item.ID, msg))
		}
		return fmt.Errorf("ingestion errors: %s", strings.Join(parts, "; "))
	}
	return nil
}

type langfuseMediaCreateRequest struct {
	TraceID       string `json:"traceId"`
	ObservationID string `json:"observationId,omitempty"`
	ContentType   string `json:"contentType"`
	ContentLength int    `json:"contentLength"`
	SHA256Hash    string `json:"sha256Hash"`
	Field         string `json:"field"`
}

type langfuseMediaCreateResponse struct {
	MediaID   string `json:"mediaId"`
	UploadURL string `json:"uploadUrl"`
}

type langfuseMediaPatchRequest struct {
	UploadedAt       string  `json:"uploadedAt"`
	UploadHTTPStatus int     `json:"uploadHttpStatus"`
	UploadHTTPError  *string `json:"uploadHttpError,omitempty"`
	UploadTimeMs     *int64  `json:"uploadTimeMs,omitempty"`
}

func (c *langfuseClient) uploadMedia(ctx context.Context, traceID, observationID, contentType string, data []byte, field string) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if !c.configured() {
		return "", fmt.Errorf("langfuse client is not configured")
	}
	if field == "" {
		field = "output"
	}
	hash := sha256.Sum256(data)
	hashB64 := base64.StdEncoding.EncodeToString(hash[:])

	createBody, err := json.Marshal(langfuseMediaCreateRequest{
		TraceID:       traceID,
		ObservationID: strings.TrimSpace(observationID),
		ContentType:   contentType,
		ContentLength: len(data),
		SHA256Hash:    hashB64,
		Field:         field,
	})
	if err != nil {
		return "", fmt.Errorf("marshal media create request: %w", err)
	}
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/public/media", bytes.NewReader(createBody))
	if err != nil {
		return "", fmt.Errorf("create media request: %w", err)
	}
	createReq.SetBasicAuth(c.publicKey, c.secretKey)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := c.httpClient.Do(createReq)
	if err != nil {
		return "", fmt.Errorf("media create request failed: %w", err)
	}
	defer createResp.Body.Close()
	createPayload, _ := io.ReadAll(io.LimitReader(createResp.Body, 1<<20))
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		return "", fmt.Errorf("media create HTTP %d: %s", createResp.StatusCode, strings.TrimSpace(string(createPayload)))
	}
	var created langfuseMediaCreateResponse
	if err := json.Unmarshal(createPayload, &created); err != nil {
		return "", fmt.Errorf("decode media create response: %w", err)
	}
	if strings.TrimSpace(created.MediaID) == "" {
		return "", fmt.Errorf("media create returned empty mediaId")
	}
	if strings.TrimSpace(created.UploadURL) != "" {
		uploadStarted := time.Now()
		uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPut, created.UploadURL, bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("create media upload request: %w", err)
		}
		uploadReq.Header.Set("Content-Type", contentType)
		uploadReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))
		uploadReq.Header.Set("x-amz-checksum-sha256", hashB64)
		uploadResp, err := c.httpClient.Do(uploadReq)
		if err != nil {
			return "", fmt.Errorf("media upload failed: %w", err)
		}
		uploadPayload, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 1<<20))
		uploadResp.Body.Close()
		uploadTimeMs := time.Since(uploadStarted).Milliseconds()
		if err := c.patchMediaUploadStatus(ctx, created.MediaID, uploadResp.StatusCode, string(uploadPayload), uploadTimeMs); err != nil {
			return "", err
		}
		if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
			return "", fmt.Errorf("media upload HTTP %d: %s", uploadResp.StatusCode, strings.TrimSpace(string(uploadPayload)))
		}
	}
	return created.MediaID, nil
}

func (c *langfuseClient) patchMediaUploadStatus(ctx context.Context, mediaID string, statusCode int, uploadBody string, uploadTimeMs int64) error {
	var uploadErr *string
	if statusCode < 200 || statusCode >= 300 {
		msg := strings.TrimSpace(uploadBody)
		if msg == "" {
			msg = fmt.Sprintf("upload failed with HTTP %d", statusCode)
		}
		uploadErr = &msg
	}
	payload, err := json.Marshal(langfuseMediaPatchRequest{
		UploadedAt:       langfuseRFC3339(time.Now().UTC()),
		UploadHTTPStatus: statusCode,
		UploadHTTPError:  uploadErr,
		UploadTimeMs:     &uploadTimeMs,
	})
	if err != nil {
		return fmt.Errorf("marshal media patch request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/api/public/media/"+mediaID, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create media patch request: %w", err)
	}
	req.SetBasicAuth(c.publicKey, c.secretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("media patch request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("media patch HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func langfuseMediaToken(contentType, mediaID string) string {
	return fmt.Sprintf("@@@langfuseMedia:type=%s|id=%s|source=bytes@@@", contentType, mediaID)
}

func langfuseRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
