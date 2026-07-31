package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleUserFiles_ServesExistingReport(t *testing.T) {
	dir := t.TempDir()
	rpt := filepath.Join(dir, "files_report.html")
	os.WriteFile(rpt, []byte("<html>cached</html>"), 0o644)
	s := &Server{userFilesReportPath: rpt}
	req := httptest.NewRequest(http.MethodGet, "/user_files", nil)
	rec := httptest.NewRecorder()
	s.handleUserFiles(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cached") {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleUserFiles_GeneratesIfMissing(t *testing.T) {
	tools := t.TempDir()
	rpt := filepath.Join(t.TempDir(), "report.html")
	script := filepath.Join(tools, "view_agent_files.sh")
	body := "#!/bin/sh\necho '<html>generated</html>' > " + rpt + "\n"
	os.WriteFile(script, []byte(body), 0o755)
	s := &Server{userFilesReportPath: rpt, userFilesToolsDir: tools}
	req := httptest.NewRequest(http.MethodGet, "/user_files", nil)
	rec := httptest.NewRecorder()
	s.handleUserFiles(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "generated") {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleUserFilesRegenerate_RunsAsync(t *testing.T) {
	tools := t.TempDir()
	rpt := filepath.Join(t.TempDir(), "report.html")
	marker := filepath.Join(t.TempDir(), "marker")
	script := filepath.Join(tools, "view_agent_files.sh")
	// 200ms sleep is long enough that a synchronous handler would not yet
	// have created the marker by the time we check immediately afterwards.
	body := "#!/bin/sh\nsleep 0.2\ntouch " + marker + "\necho '<html>regen</html>' > " + rpt + "\n"
	os.WriteFile(script, []byte(body), 0o755)
	s := &Server{userFilesReportPath: rpt, userFilesToolsDir: tools}
	req := httptest.NewRequest(http.MethodPost, "/user_files/regenerate", nil)
	rec := httptest.NewRecorder()
	s.handleUserFilesRegenerate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// Handler must return before the script finishes; marker must not exist yet.
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("handler waited for script completion; expected async fire-and-forget")
	}
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("script did not run within timeout")
}

func TestHandleUserFilesPreview_ServesImage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "screens", "frame.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("\x89PNG\r\n\x1a\npreview")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{userFilesMemoryDir: root}
	req := httptest.NewRequest(http.MethodGet, "/user_files/preview?type=memory&path=screens/frame.png", nil)
	rec := httptest.NewRecorder()
	s.handleUserFilesPreview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "image/png") {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("Content-Security-Policy = %q, want restrictive policy", got)
	}
	if got := rec.Body.Bytes(); string(got) != string(want) {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandleUserFilesPreview_RejectsNonImage(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string][]byte{
		"note.txt": []byte("hello"),
		"icon.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
	} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{"note.txt", "icon.svg"} {
		s := &Server{userFilesMemoryDir: root}
		req := httptest.NewRequest(http.MethodGet, "/user_files/preview?type=memory&path="+path, nil)
		rec := httptest.NewRecorder()
		s.handleUserFilesPreview(rec, req)

		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s: status %d body=%q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleUserFilesPreview_RejectsInvalidRequests(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "screens"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "screens", "frame.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		url  string
	}{
		{name: "path traversal", url: "/user_files/preview?type=memory&path=../frame.png"},
		{name: "absolute path", url: "/user_files/preview?type=memory&path=/tmp/frame.png"},
		{name: "unknown file type", url: "/user_files/preview?type=unknown&path=screens/frame.png"},
		{name: "missing path", url: "/user_files/preview?type=memory&path=screens/missing.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{userFilesMemoryDir: root}
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			rec := httptest.NewRecorder()
			s.handleUserFilesPreview(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d body=%q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
				t.Fatalf("Content-Security-Policy = %q, want restrictive policy", got)
			}
		})
	}
}

func TestHandleUserFilesPreview_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	s := &Server{userFilesMemoryDir: root}
	req := httptest.NewRequest(http.MethodGet, "/user_files/preview?type=memory&path=link.png", nil)
	rec := httptest.NewRecorder()
	s.handleUserFilesPreview(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body=%q", rec.Code, rec.Body.String())
	}
}
