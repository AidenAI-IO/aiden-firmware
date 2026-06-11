package agent

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

const userFilesGenerateTimeout = 30 * time.Second

// runUserFilesScript runs view_agent_files.sh under a fixed timeout, killing
// the process if it exceeds the deadline. Output is collected for diagnostics.
// Used by both ensureUserFilesReport (synchronous) and handleUserFilesRegenerate
// (async) so a hung script can never leak a long-lived child.
func (s *Server) runUserFilesScript(stdoutSink string) error {
	script := "cd " + shellQuote(s.userFilesToolsDir) + " && ./view_agent_files.sh"
	if stdoutSink != "" {
		script += " > " + shellQuote(stdoutSink) + " 2>&1"
	} else {
		script += " 2>&1"
	}
	cmd := exec.Command("sh", "-c", script)
	timer := time.AfterFunc(userFilesGenerateTimeout, func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	if stdoutSink != "" {
		// Caller redirected stdout to a file; we only need exit status.
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("user_files generation failed: %s", string(out))
	}
	return nil
}

func (s *Server) ensureUserFilesReport() error {
	if _, err := os.Stat(s.userFilesReportPath); err == nil {
		return nil
	}
	if err := s.runUserFilesScript(""); err != nil {
		return err
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
		// Reuse the same timeout budget so a hung script cannot pile up
		// long-lived background generators on repeated calls.
		_ = s.runUserFilesScript("/tmp/user_files_regenerate.log")
	}()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"regenerating":true}`))
}
