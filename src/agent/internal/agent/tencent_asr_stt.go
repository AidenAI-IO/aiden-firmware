package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	tencentASRHost    = "asr.tencentcloudapi.com"
	tencentASRService = "asr"
	tencentASRAction   = "SentenceRecognition"
	tencentASRVersion  = "2019-06-14"
	tencentASRAlgorithm = "TC3-HMAC-SHA256"
)

// TencentASRSTT implements one-shot sentence recognition via Tencent Cloud ASR API v3.
type TencentASRSTT struct {
	secretID        string
	secretKey       string
	region          string
	engineModelType string
	httpClient      *http.Client
	apiURL          string // optional override for tests
}

// NewTencentASRSTT creates a Tencent ASR STT client.
func NewTencentASRSTT(secretID, secretKey, region, engineModelType string, httpClients ...*http.Client) *TencentASRSTT {
	if region == "" {
		region = "ap-guangzhou"
	}
	if engineModelType == "" {
		engineModelType = "16k_zh"
	}
	httpClient := http.DefaultClient
	if len(httpClients) > 0 && httpClients[0] != nil {
		httpClient = httpClients[0]
	}
	return &TencentASRSTT{
		secretID:        strings.TrimSpace(secretID),
		secretKey:       strings.TrimSpace(secretKey),
		region:          region,
		engineModelType: engineModelType,
		httpClient:      httpClient,
	}
}

func (s *TencentASRSTT) TranscribeWAV(wavData []byte) (string, error) {
	if len(wavData) == 0 {
		return "", fmt.Errorf("empty wav data")
	}
	if s.secretID == "" || s.secretKey == "" {
		return "", fmt.Errorf("tencent ASR: secret_id and secret_key are required")
	}

	payload, err := json.Marshal(tencentASRRequest{
		EngSerViceType: s.engineModelType,
		SourceType:     1,
		VoiceFormat:    "wav",
		Data:           base64.StdEncoding.EncodeToString(wavData),
		DataLen:        len(wavData),
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	timestamp := time.Now().Unix()
	authorization, err := buildTencentTC3Authorization(s.secretID, s.secretKey, string(payload), timestamp)
	if err != nil {
		return "", err
	}

	apiURL := s.apiURL
	if apiURL == "" {
		apiURL = "https://" + tencentASRHost
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Host = tencentASRHost
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-TC-Action", tencentASRAction)
	req.Header.Set("X-TC-Version", tencentASRVersion)
	req.Header.Set("X-TC-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-TC-Region", s.region)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var parsed tencentASRResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if parsed.Response.Error != nil {
		msg := strings.TrimSpace(parsed.Response.Error.Message)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Response.Error.Code)
		}
		return "", fmt.Errorf("tencent ASR API error: %s", msg)
	}
	text := strings.TrimSpace(parsed.Response.Result)
	if text == "" {
		return "", fmt.Errorf("tencent ASR returned empty transcript")
	}
	return text, nil
}

type tencentASRRequest struct {
	EngSerViceType string `json:"EngSerViceType"`
	SourceType     int    `json:"SourceType"`
	VoiceFormat    string `json:"VoiceFormat"`
	Data           string `json:"Data"`
	DataLen        int    `json:"DataLen"`
}

type tencentASRResponse struct {
	Response struct {
		Result string `json:"Result"`
		Error  *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

func buildTencentTC3Authorization(secretID, secretKey, payload string, timestamp int64) (string, error) {
	if secretID == "" || secretKey == "" {
		return "", fmt.Errorf("tencent ASR: missing credentials")
	}

	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	ts := fmt.Sprintf("%d", timestamp)

	canonicalHeaders := "content-type:application/json; charset=utf-8\nhost:" + tencentASRHost + "\n"
	signedHeaders := "content-type;host"
	hashedPayload := sha256Hex(payload)
	canonicalRequest := strings.Join([]string{
		"POST",
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		hashedPayload,
	}, "\n")

	credentialScope := date + "/" + tencentASRService + "/tc3_request"
	stringToSign := strings.Join([]string{
		tencentASRAlgorithm,
		ts,
		credentialScope,
		sha256Hex(canonicalRequest),
	}, "\n")

	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, tencentASRService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))

	return fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		tencentASRAlgorithm,
		secretID,
		credentialScope,
		signedHeaders,
		signature,
	), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(msg))
	return mac.Sum(nil)
}
