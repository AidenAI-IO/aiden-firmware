package agent

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

const userFilesGenerateTimeout = 30 * time.Second

func (s *Server) ensureUserFilesReport() error {
	if _, err := os.Stat(s.userFilesReportPath); err == nil {
		return nil
	}
	cmd := exec.Command("sh", "-c", "cd "+shellQuote(s.userFilesToolsDir)+" && ./view_agent_files.sh 2>&1")
	timer := time.AfterFunc(userFilesGenerateTimeout, func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	out, err := cmd.CombinedOutput()
	timer.Stop()
	if err != nil {
		return fmt.Errorf("user_files generation failed: %s", string(out))
	}
	if _, err := os.Stat(s.userFilesReportPath); err != nil {
		return fmt.Errorf("script ran but report not produced")
	}
	return nil
}

func (s *Server) handleUserFiles(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureUserFilesReport(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := os.ReadFile(s.userFilesReportPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleUserFilesRegenerate(w http.ResponseWriter, r *http.Request) {
	go func() {
		cmd := exec.Command("sh", "-c", "cd "+shellQuote(s.userFilesToolsDir)+" && ./view_agent_files.sh > /tmp/user_files_regenerate.log 2>&1")
		cmd.Run()
	}()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"regenerating":true}`))
}
