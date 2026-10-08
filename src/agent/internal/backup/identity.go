package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// OTAIdentityProvisioner re-runs the OTA identity provisioning command after
// a machine-id restore.  It is shared by Config Web and the boot recovery.
type OTAIdentityProvisioner struct {
	Binary     string
	ConfigPath string
	Timeout    time.Duration
}

// ProvisionIdentity runs `ota provision-identity` and reports whether the
// device must reboot to finish provisioning.  An empty Binary disables the
// step, which host tests rely on.
func (p OTAIdentityProvisioner) ProvisionIdentity(ctx context.Context) (bool, error) {
	binary := strings.TrimSpace(p.Binary)
	if binary == "" {
		return false, nil
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{}
	if strings.TrimSpace(p.ConfigPath) != "" {
		args = append(args, "--config", p.ConfigPath)
	}
	args = append(args, "provision-identity")
	output, err := exec.CommandContext(ctx, binary, args...).Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, errors.New("provision-identity timed out")
	}
	if err != nil {
		return false, fmt.Errorf("provision-identity: %w", err)
	}
	var payload struct {
		RebootRequired bool `json:"reboot_required"`
	}
	text := string(output)
	if index := strings.Index(text, "{"); index >= 0 {
		text = text[index:]
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return false, fmt.Errorf("provision-identity output is not JSON")
	}
	return payload.RebootRequired, nil
}
