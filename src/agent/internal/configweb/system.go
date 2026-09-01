package configweb

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func (s *Server) handleReboot(w http.ResponseWriter, _ *http.Request) {
	if _, err := exec.LookPath("reboot"); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "reboot command not found")
		return
	}
	cmd := exec.Command("/bin/sh", "-c", "sync; sleep 1; reboot")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "failed to schedule reboot: "+err.Error())
		return
	}
	go func() { _ = cmd.Wait() }()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "reboot scheduled", "reboot_scheduled": true})
}

const usbReenumerateScript = `set -eu
UDC="$(ls /sys/class/udc 2>/dev/null | head -n 1)"
GADGET_DIR=/sys/kernel/config/usb_gadget/aiden_hid
if [ -z "$UDC" ] || [ ! -d "$GADGET_DIR" ]; then
  echo 'Error: UDC device or gadget directory not found' >&2
  exit 1
fi
if ! echo "" > "$GADGET_DIR/UDC" 2>/dev/null; then
  echo 'Error: Failed to unbind UDC' >&2
  exit 2
fi
sleep 1
if ! echo "$UDC" > "$GADGET_DIR/UDC" 2>/dev/null; then
  echo 'Error: Failed to rebind UDC' >&2
  exit 3
fi
echo 'USB re-enumeration completed successfully'
`

func (s *Server) handleUSBReenumerate(w http.ResponseWriter, _ *http.Request) {
	result := runCommand(5*time.Second, nil, []byte(usbReenumerateScript), "sh", "-s")
	if result.ExitCode != 0 {
		message := strings.TrimSpace(string(result.Output))
		if message == "" {
			message = fmt.Sprintf("USB re-enumeration failed with exit code %d", result.ExitCode)
		}
		writeJSONError(w, 500, message)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "message": "USB re-enumeration triggered successfully"})
}

func (s *Server) prepareOTAUpdateLog() (int64, error) {
	if err := os.MkdirAll(filepath.Dir(s.options.OTAUpdateLogPath), 0o755); err != nil {
		return 0, err
	}
	if info, err := os.Stat(s.options.OTAUpdateLogPath); err == nil && info.Size() > 100*1024 {
		data, err := tailFile(s.options.OTAUpdateLogPath, 100*1024)
		if err != nil {
			return 0, err
		}
		if err := atomicWriteFile(s.options.OTAUpdateLogPath, data, 0o600); err != nil {
			return 0, err
		}
	}
	file, err := os.OpenFile(s.options.OTAUpdateLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *Server) acquireOTALock() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(s.options.OTAUpdateLockPath), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.options.OTAUpdateLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fmt.Errorf("ota update already running")
		}
		return nil, err
	}
	return file, nil
}

func (s *Server) otaUpdateRunning() bool {
	file, err := os.OpenFile(s.options.OTAUpdateLockPath, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false
}

func (s *Server) handleOTAUpdate(w http.ResponseWriter, _ *http.Request) {
	lock, err := s.acquireOTALock()
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	startSize, err := s.prepareOTAUpdateLog()
	if err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		writeJSONError(w, 500, err.Error())
		return
	}
	logPath := s.options.OTAUpdateLogPath
	otaBinary := s.options.OTABinary
	envRun := s.options.EnvRunBinary
	go func() {
		defer lock.Close()
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		defer logFile.Close()
		name := otaBinary
		args := []string{"update"}
		if info, statErr := os.Stat(envRun); statErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			name = envRun
			args = []string{otaBinary, "update"}
		}
		cmd := exec.Command(name, args...)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		runErr := cmd.Run()
		exitCode := 0
		if runErr != nil {
			exitCode = 1
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}
		level := "INFO"
		if exitCode != 0 {
			level = "ERROR"
		}
		fmt.Fprintf(logFile, "%s [%s] [config_web] [ota] update_exited exit_code=%d\n", time.Now().UTC().Format(time.RFC3339), level, exitCode)
	}()
	writeJSON(w, 200, map[string]any{"ok": true, "ota_update_started": true, "message": "ota update started", "ota_log_start_size_bytes": startSize})
}

func (s *Server) handleOTALogs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "ota_update_running": s.otaUpdateRunning(), "ota_log": readLogSnapshot(s.options.OTAUpdateLogPath, 128*1024, 96*1024), "ota_health_log": readLogSnapshot(s.options.OTAHealthLogPath, 128*1024, 96*1024)})
}
