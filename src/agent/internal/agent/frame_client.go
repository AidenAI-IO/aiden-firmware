package agent

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// FrameServiceClient communicates with the frame_service via Unix domain socket.
type FrameServiceClient struct {
	socketPath string
}

func NewFrameServiceClient(socketPath string) *FrameServiceClient {
	if socketPath == "" {
		socketPath = defaultFrameServiceSocket
	}
	return &FrameServiceClient{socketPath: socketPath}
}

type frameMetadata struct {
	Seq          uint64 `json:"seq"`
	Width        uint32 `json:"width"`
	Height       uint32 `json:"height"`
	SourceWidth  uint32 `json:"source_width,omitempty"`
	SourceHeight uint32 `json:"source_height,omitempty"`
	CropX        uint32 `json:"crop_x,omitempty"`
	CropY        uint32 `json:"crop_y,omitempty"`
	CropWidth    uint32 `json:"crop_width,omitempty"`
	CropHeight   uint32 `json:"crop_height,omitempty"`
	PixelFormat  string `json:"pixel_format"`
	Stride       uint32 `json:"stride"`
	Bytes        uint64 `json:"bytes"`
	Stale        bool   `json:"stale"`
}

func (m *frameMetadata) UnmarshalJSON(data []byte) error {
	var raw struct {
		Seq          json.RawMessage `json:"seq"`
		Width        json.RawMessage `json:"width"`
		Height       json.RawMessage `json:"height"`
		SourceWidth  json.RawMessage `json:"source_width"`
		SourceHeight json.RawMessage `json:"source_height"`
		CropX        json.RawMessage `json:"crop_x"`
		CropY        json.RawMessage `json:"crop_y"`
		CropWidth    json.RawMessage `json:"crop_width"`
		CropHeight   json.RawMessage `json:"crop_height"`
		PixelFormat  string          `json:"pixel_format"`
		Stride       json.RawMessage `json:"stride"`
		Bytes        json.RawMessage `json:"bytes"`
		Stale        json.RawMessage `json:"stale"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	seq, err := parseFlexibleUint64(raw.Seq)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	width, err := parseFlexibleUint32(raw.Width)
	if err != nil {
		return fmt.Errorf("width: %w", err)
	}
	height, err := parseFlexibleUint32(raw.Height)
	if err != nil {
		return fmt.Errorf("height: %w", err)
	}
	sourceWidth, err := parseFlexibleUint32(raw.SourceWidth)
	if err != nil {
		return fmt.Errorf("source_width: %w", err)
	}
	sourceHeight, err := parseFlexibleUint32(raw.SourceHeight)
	if err != nil {
		return fmt.Errorf("source_height: %w", err)
	}
	cropX, err := parseFlexibleUint32(raw.CropX)
	if err != nil {
		return fmt.Errorf("crop_x: %w", err)
	}
	cropY, err := parseFlexibleUint32(raw.CropY)
	if err != nil {
		return fmt.Errorf("crop_y: %w", err)
	}
	cropWidth, err := parseFlexibleUint32(raw.CropWidth)
	if err != nil {
		return fmt.Errorf("crop_width: %w", err)
	}
	cropHeight, err := parseFlexibleUint32(raw.CropHeight)
	if err != nil {
		return fmt.Errorf("crop_height: %w", err)
	}
	stride, err := parseFlexibleUint32(raw.Stride)
	if err != nil {
		return fmt.Errorf("stride: %w", err)
	}
	sizeBytes, err := parseFlexibleUint64(raw.Bytes)
	if err != nil {
		return fmt.Errorf("bytes: %w", err)
	}
	stale, err := parseFlexibleBool(raw.Stale)
	if err != nil {
		return fmt.Errorf("stale: %w", err)
	}

	m.Seq = seq
	m.Width = width
	m.Height = height
	m.SourceWidth = sourceWidth
	m.SourceHeight = sourceHeight
	m.CropX = cropX
	m.CropY = cropY
	m.CropWidth = cropWidth
	m.CropHeight = cropHeight
	m.PixelFormat = raw.PixelFormat
	m.Stride = stride
	m.Bytes = sizeBytes
	m.Stale = stale
	return nil
}

type frameResponse struct {
	Status string        `json:"status"`
	Frame  frameMetadata `json:"frame"`
}

// LatestFrame fetches the most recent frame from the service.
func (c *FrameServiceClient) LatestFrame() (*frameMetadata, []byte, error) {
	return c.LatestFrameWithFormat("raw", 0)
}

// LatestFrameWithFormat fetches the most recent frame with specified format.
// format: "raw" (YUV) or "jpeg"
// quality: JPEG quality (1-100), ignored for raw format
func (c *FrameServiceClient) LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, error) {
	if format == "" {
		format = "raw"
	}
	if quality <= 0 {
		quality = 80
	}

	request := fmt.Sprintf(`{"type":"request","method":"latest_frame","since_seq":"0","timeout_ms":0,"format":"%s","quality":%d}`, format, quality)

	headerJSON, payload, err := c.doRequest(request, nil, 5*time.Second)
	if err != nil {
		return nil, nil, err
	}

	var resp frameResponse
	if err := json.Unmarshal(headerJSON, &resp); err != nil {
		return nil, nil, fmt.Errorf("parse response: %w", err)
	}
	if resp.Status != "OK" {
		return nil, nil, fmt.Errorf("frame service: %s", resp.Status)
	}
	if uint64(len(payload)) != resp.Frame.Bytes {
		return nil, nil, fmt.Errorf("payload size mismatch: got %d, expected %d", len(payload), resp.Frame.Bytes)
	}
	// Return stale frame but let caller check meta.Stale flag
	return &resp.Frame, payload, nil
}

func (c *FrameServiceClient) doRequest(requestJSON string, requestPayload []byte, timeout time.Duration) ([]byte, []byte, error) {
	conn, err := net.DialTimeout("unix", c.socketPath, timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", c.socketPath, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	if err := writeUDSMessage(conn, []byte(requestJSON), requestPayload); err != nil {
		return nil, nil, err
	}

	return readUDSMessage(conn)
}

// Wire protocol: [header_len LE32][payload_len LE64][header bytes][payload bytes]
func writeUDSMessage(w io.Writer, header, payload []byte) error {
	prefix := make([]byte, 12)
	binary.LittleEndian.PutUint32(prefix[0:4], uint32(len(header)))
	binary.LittleEndian.PutUint64(prefix[4:12], uint64(len(payload)))

	if _, err := w.Write(prefix); err != nil {
		return fmt.Errorf("write prefix: %w", err)
	}
	if len(header) > 0 {
		if _, err := w.Write(header); err != nil {
			return fmt.Errorf("write header: %w", err)
		}
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("write payload: %w", err)
		}
	}
	return nil
}

func readUDSMessage(r io.Reader) (header []byte, payload []byte, err error) {
	prefix := make([]byte, 12)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, nil, fmt.Errorf("read prefix: %w", err)
	}

	headerLen := binary.LittleEndian.Uint32(prefix[0:4])
	payloadLen := binary.LittleEndian.Uint64(prefix[4:12])

	const maxHeader = 1024 * 1024
	const maxPayload = 64 * 1024 * 1024
	if headerLen > maxHeader || payloadLen > maxPayload {
		return nil, nil, fmt.Errorf("message too large: header=%d payload=%d", headerLen, payloadLen)
	}

	header = make([]byte, headerLen)
	if headerLen > 0 {
		if _, err := io.ReadFull(r, header); err != nil {
			return nil, nil, fmt.Errorf("read header: %w", err)
		}
	}

	payload = make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, nil, fmt.Errorf("read payload: %w", err)
		}
	}

	return header, payload, nil
}

func parseFlexibleUint64(data json.RawMessage) (uint64, error) {
	if len(data) == 0 || string(data) == "null" {
		return 0, nil
	}

	var asUint uint64
	if err := json.Unmarshal(data, &asUint); err == nil {
		return asUint, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		value, parseErr := strconv.ParseUint(asString, 10, 64)
		if parseErr != nil {
			return 0, parseErr
		}
		return value, nil
	}

	return 0, fmt.Errorf("unsupported value %s", string(data))
}

func parseFlexibleUint32(data json.RawMessage) (uint32, error) {
	value, err := parseFlexibleUint64(data)
	if err != nil {
		return 0, err
	}
	if value > uint64(^uint32(0)) {
		return 0, fmt.Errorf("value %d overflows uint32", value)
	}
	return uint32(value), nil
}

func parseFlexibleBool(data json.RawMessage) (bool, error) {
	if len(data) == 0 || string(data) == "null" {
		return false, nil
	}

	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		return asBool, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		value, parseErr := strconv.ParseBool(asString)
		if parseErr != nil {
			return false, parseErr
		}
		return value, nil
	}

	return false, fmt.Errorf("unsupported value %s", string(data))
}
