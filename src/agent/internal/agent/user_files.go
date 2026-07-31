package agent

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func (s *Server) handleUserFilesPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; sandbox")
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	root := s.userFilesRoot(r.URL.Query().Get("type"))
	if root == "" {
		http.Error(w, "invalid file type", http.StatusBadRequest)
		return
	}

	filePath, err := safeUserFilesPath(root, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	contentType := userFilesImageContentType(filePath)
	if contentType == "" {
		http.Error(w, "unsupported preview type", http.StatusUnsupportedMediaType)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filePath)
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

func (s *Server) userFilesRoot(kind string) string {
	switch strings.TrimSpace(kind) {
	case "memory":
		if s.userFilesMemoryDir != "" {
			return s.userFilesMemoryDir
		}
		return userFilesRootFromReportPath(s.userFilesReportPath, "memory")
	case "skills":
		if s.userFilesSkillsDir != "" {
			return s.userFilesSkillsDir
		}
		return userFilesRootFromReportPath(s.userFilesReportPath, "skills")
	case "skill-state":
		if s.userFilesSkillStateDir != "" {
			return s.userFilesSkillStateDir
		}
		return userFilesRootFromReportPath(s.userFilesReportPath, "skill-state")
	default:
		return ""
	}
}

func userFilesRootFromReportPath(reportPath, name string) string {
	base := strings.TrimSpace(filepath.Dir(reportPath))
	if base == "" || base == "." {
		return ""
	}
	return filepath.Join(base, name)
}

func safeUserFilesPath(root, rel string) (string, error) {
	root = strings.TrimSpace(root)
	rel = strings.TrimSpace(rel)
	if root == "" || rel == "" {
		return "", fmt.Errorf("empty path")
	}

	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path")
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, cleanRel))
	if err != nil {
		return "", err
	}

	relToRoot, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relToRoot) {
		return "", fmt.Errorf("path escapes root")
	}
	return resolvedPath, nil
}

func userFilesImageContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".svg" {
		return ""
	}
	if contentType := mime.TypeByExtension(ext); strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return contentType
	}
	switch ext {
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	case ".gif":
		return "image/gif"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}
