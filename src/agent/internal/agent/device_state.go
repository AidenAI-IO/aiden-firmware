package agent

type deviceStateUpdater struct {
	config Config
}

func newDeviceStateUpdater(config Config) *deviceStateUpdater {
	return &deviceStateUpdater{config: config}
}

func (u *deviceStateUpdater) UpdateState() map[string]string {
	if u == nil {
		return nil
	}
	return map[string]string{
		"device_type":         u.config.DeviceTypeOrDefault(),
		"device_platform":     u.config.DevicePlatformOrDefault(),
		"device_pointer_mode": u.config.PointerModeOrDefault(),
	}
}
