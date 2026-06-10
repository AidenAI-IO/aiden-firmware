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
	body := "#!/bin/sh\nsleep 0.1\ntouch " + marker + "\necho '<html>regen</html>' > " + rpt + "\n"
	os.WriteFile(script, []byte(body), 0o755)
	s := &Server{userFilesReportPath: rpt, userFilesToolsDir: tools}
	req := httptest.NewRequest(http.MethodPost, "/user_files/regenerate", nil)
	rec := httptest.NewRecorder()
	s.handleUserFilesRegenerate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("script did not run within timeout")
}
