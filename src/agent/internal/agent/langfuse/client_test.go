package langfuse

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadMediaCompletesWithPatch(t *testing.T) {
	var patchCalled bool
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("upload server method = %s, want PUT", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	langfuseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/media":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"mediaId":"media-abc","uploadUrl":"` + uploadServer.URL + `"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/public/media/media-abc":
			patchCalled = true
			body, _ := io.ReadAll(r.Body)
			var patch MediaPatchRequest
			if err := json.Unmarshal(body, &patch); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			if patch.UploadHTTPStatus != http.StatusOK {
				t.Fatalf("UploadHTTPStatus = %d, want 200", patch.UploadHTTPStatus)
			}
			if strings.TrimSpace(patch.UploadedAt) == "" {
				t.Fatal("expected uploadedAt in patch body")
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer langfuseServer.Close()

	client := NewClient(Config{
		BaseURL:   langfuseServer.URL,
		PublicKey: "pk-test",
		SecretKey: "sk-test",
	})

	mediaID, err := client.UploadMedia(context.Background(), "trace-1", "observation-1", "image/jpeg", []byte("jpeg-bytes"), "output")
	if err != nil {
		t.Fatalf("uploadMedia() error = %v", err)
	}
	if mediaID != "media-abc" {
		t.Fatalf("mediaID = %q, want media-abc", mediaID)
	}
	if !patchCalled {
		t.Fatal("expected PATCH /api/public/media/{mediaId} after PUT upload")
	}
}
