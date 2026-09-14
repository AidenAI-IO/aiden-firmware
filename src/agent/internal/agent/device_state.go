package agent

type deviceStateUpdater struct {
	config   Config
	snapshot func() Config
}

func newDeviceStateUpdater(config Config) *deviceStateUpdater {
	return &deviceStateUpdater{config: config}
}

func (u *deviceStateUpdater) UpdateState() map[string]string {
	if u == nil {
		return nil
	}
	cfg := u.config
	if u.snapshot != nil {
		cfg = u.snapshot()
	}
	return map[string]string{
		"device_type":         cfg.DeviceTypeOrDefault(),
		"device_platform":     cfg.DevicePlatformOrDefault(),
		"device_pointer_mode": cfg.PointerModeOrDefault(),
	}
}
