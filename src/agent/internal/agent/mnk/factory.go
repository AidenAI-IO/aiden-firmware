package mnk

import (
	"aiden-agent/internal/agent/screen"
)

// ProviderFactory 用于根据配置创建合适的 Provider
type ProviderFactory struct {
	screenState *screen.ScreenState
}

// NewProviderFactory 创建 Provider 工厂
func NewProviderFactory(screenState *screen.ScreenState) *ProviderFactory {
	return &ProviderFactory{
		screenState: screenState,
	}
}

// CreateHIDProvider 创建 HID Provider（用于 USB HID 控制）。
// 会新建 HID 设备；生产路径应优先用 CreateHIDProviderWithDevices 共享设备与 isolation。
func (f *ProviderFactory) CreateHIDProvider(
	pointerDevice string,
	keyboardDevice string,
	androidKeyboardDevice string,
	touchscreen bool,
	keyboardLayout string,
) (Provider, error) {
	pointerDev := NewHIDDevice(pointerDevice)
	keyboardDev := NewHIDDevice(keyboardDevice)
	var androidKeyboardDev Device
	if androidKeyboardDevice != "" {
		androidKeyboardDev = NewHIDDevice(androidKeyboardDevice)
	}
	return f.CreateHIDProviderWithDevices(pointerDev, keyboardDev, androidKeyboardDev, touchscreen, keyboardLayout, nil)
}

// CreateHIDProviderWithDevices builds an HID provider from already-owned devices and an optional ProfileGate.
func (f *ProviderFactory) CreateHIDProviderWithDevices(
	pointerDev, keyboardDev, androidKeyboardDev Device,
	touchscreen bool,
	keyboardLayout string,
	gate ProfileGate,
) (Provider, error) {
	return NewHIDProvider(
		pointerDev,
		keyboardDev,
		androidKeyboardDev,
		f.screenState,
		touchscreen,
		keyboardLayout,
		gate,
	), nil
}

// CreateADBProvider 创建 ADB Provider（用于 adb shell input 控制）
func (f *ProviderFactory) CreateADBProvider() (Provider, error) {
	provider := NewADBProvider(f.screenState, nil, nil)
	return provider, nil
}

// CreateHTTPProvider 创建 HTTP Provider（用于远程 HTTP 控制）
func (f *ProviderFactory) CreateHTTPProvider(baseURL string, taskID string) (Provider, error) {
	provider := NewHTTPProvider(HTTPProviderConfig{
		BaseURL: baseURL,
		TaskID:  taskID,
	})
	return provider, nil
}

// ProviderConfig 配置结构
type ProviderConfig struct {
	// Backend 类型: "hid", "adb", 或 "http"
	Backend string

	// HID 配置
	PointerDevice         string
	KeyboardDevice        string
	AndroidKeyboardDevice string
	Touchscreen           bool
	KeyboardLayout        string

	// HTTP 配置
	HTTPBaseURL string
	HTTPTaskID  string
}

// CreateProvider 根据配置创建 Provider
func (f *ProviderFactory) CreateProvider(config ProviderConfig) (Provider, error) {
	switch config.Backend {
	case "hid":
		return f.CreateHIDProvider(
			config.PointerDevice,
			config.KeyboardDevice,
			config.AndroidKeyboardDevice,
			config.Touchscreen,
			config.KeyboardLayout,
		)
	case "adb":
		return f.CreateADBProvider()
	case "http":
		return f.CreateHTTPProvider(config.HTTPBaseURL, config.HTTPTaskID)
	default:
		// 默认使用 HID
		return f.CreateHIDProvider(
			config.PointerDevice,
			config.KeyboardDevice,
			config.AndroidKeyboardDevice,
			config.Touchscreen,
			config.KeyboardLayout,
		)
	}
}
