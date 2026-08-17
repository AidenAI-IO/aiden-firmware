package mnk_test

import (
	"aiden-agent/internal/agent/mnk"
	"aiden-agent/internal/agent/screen"
	"context"
	"fmt"
	"log"
)

// ExampleHIDProvider 展示如何创建和使用 HID Provider
func ExampleHIDProvider() {
	// 创建 screen state（用于坐标转换）
	screenState := &screen.ScreenState{}
	screenState.Update(1920, 1080) // 设置屏幕尺寸

	// 创建 Provider Factory
	factory := mnk.NewProviderFactory(screenState)

	// 创建 HID Provider
	provider, err := factory.CreateHIDProvider(
		"/dev/hidg0",  // 指针设备
		"/dev/hidg1",  // 键盘设备
		"/dev/hidg2",  // Android 扩展键盘设备
		true,          // touchscreen 模式
		"qwerty",      // 键盘布局
	)
	if err != nil {
		log.Fatalf("创建 HID Provider 失败: %v", err)
	}

	// 使用示例

	// 1. 点击
	err = provider.Click(500, 500, "left", 0)
	if err != nil {
		log.Printf("点击失败: %v", err)
	}

	// 2. 长按
	err = provider.Click(500, 500, "left", 500) // 按住 500ms
	if err != nil {
		log.Printf("长按失败: %v", err)
	}

	// 3. 双击
	err = provider.DoubleClick(300, 400, "left")
	if err != nil {
		log.Printf("双击失败: %v", err)
	}

	// 4. 简单滑动
	err = provider.Drag([][2]float64{
		{100, 500}, // 起点
		{900, 500}, // 终点
	}, "left")
	if err != nil {
		log.Printf("滑动失败: %v", err)
	}

	// 5. 曲线手势（多点路径）
	err = provider.Drag([][2]float64{
		{100, 500},
		{300, 300},
		{700, 300},
		{900, 500},
	}, "left")
	if err != nil {
		log.Printf("曲线手势失败: %v", err)
	}

	// 6. 按键
	err = provider.Keypress([]string{"enter"})
	if err != nil {
		log.Printf("按键失败: %v", err)
	}

	// 7. 组合键
	err = provider.Keypress([]string{"ctrl", "a"})
	if err != nil {
		log.Printf("组合键失败: %v", err)
	}

	// 8. 光标移动（仅 absolute mode）
	err = provider.Move(500, 300)
	if err != nil {
		log.Printf("移动失败: %v", err)
	}

	// 9. 滚动
	err = provider.Scroll(0, -3)
	if err != nil {
		log.Printf("滚动失败: %v", err)
	}
}

// ExampleADBProvider 展示如何创建和使用 ADB Provider
func ExampleADBProvider() {
	// 创建 screen state
	screenState := &screen.ScreenState{}
	screenState.Update(1080, 1920)

	// 创建 Provider Factory
	factory := mnk.NewProviderFactory(screenState)

	// 创建 ADB Provider
	provider, err := factory.CreateADBProvider()
	if err != nil {
		log.Fatalf("创建 ADB Provider 失败: %v", err)
	}

	// 使用方法与 HID Provider 完全相同
	err = provider.Click(500, 500, "left", 0)
	if err != nil {
		log.Printf("点击失败: %v", err)
	}

	err = provider.Drag([][2]float64{
		{500, 800},
		{500, 200},
	}, "left")
	if err != nil {
		log.Printf("滑动失败: %v", err)
	}

	err = provider.Keypress([]string{"android_back"})
	if err != nil {
		log.Printf("返回键失败: %v", err)
	}
}

// Example_toolIntegration 展示如何在工具中集成 MNK Provider
func Example_toolIntegration() {
	// 创建 Provider
	screenState := &screen.ScreenState{}
	screenState.Update(1920, 1080)
	factory := mnk.NewProviderFactory(screenState)
	provider, _ := factory.CreateHIDProvider(
		"/dev/hidg0",
		"/dev/hidg1",
		"/dev/hidg2",
		true,
		"qwerty",
	)

	// 创建工具适配器
	touchGestureTool := mnk.NewTouchGestureToolAdapter(provider, func() string {
		return "android"
	})

	keyboardTapTool := mnk.NewKeyboardTapToolAdapter(provider)
	mouseMoveTool := mnk.NewMouseMoveToolAdapter(provider)
	mouseScrollTool := mnk.NewMouseScrollToolAdapter(provider)

	// 使用工具
	ctx := context.Background()

	// Touch gesture - tap
	result, err := touchGestureTool.Call(ctx, `{"type":"tap","point":{"x":500,"y":500}}`)
	fmt.Printf("Tap result: %s, error: %v\n", result, err)

	// Touch gesture - swipe
	result, err = touchGestureTool.Call(ctx, `{"type":"swipe","start":{"x":100,"y":500},"end":{"x":900,"y":500}}`)
	fmt.Printf("Swipe result: %s, error: %v\n", result, err)

	// Keyboard tap
	result, err = keyboardTapTool.Call(ctx, `{"keys":["ctrl","a"]}`)
	fmt.Printf("Keyboard result: %s, error: %v\n", result, err)

	// Mouse move
	result, err = mouseMoveTool.Call(ctx, `{"x":250,"y":750}`)
	fmt.Printf("Move result: %s, error: %v\n", result, err)

	// Mouse scroll
	result, err = mouseScrollTool.Call(ctx, `{"delta":-3}`)
	fmt.Printf("Scroll result: %s, error: %v\n", result, err)
}

// ExampleProviderFactory 展示如何使用配置创建 Provider
func ExampleProviderFactory() {
	screenState := &screen.ScreenState{}
	screenState.Update(1920, 1080)
	factory := mnk.NewProviderFactory(screenState)

	// 配置 HID Provider
	hidConfig := mnk.ProviderConfig{
		Backend:               "hid",
		PointerDevice:         "/dev/hidg0",
		KeyboardDevice:        "/dev/hidg1",
		AndroidKeyboardDevice: "/dev/hidg2",
		Touchscreen:           true,
		KeyboardLayout:        "qwerty",
	}

	hidProvider, err := factory.CreateProvider(hidConfig)
	if err != nil {
		log.Fatalf("创建 HID Provider 失败: %v", err)
	}

	// 配置 ADB Provider
	adbConfig := mnk.ProviderConfig{
		Backend: "adb",
	}

	adbProvider, err := factory.CreateProvider(adbConfig)
	if err != nil {
		log.Fatalf("创建 ADB Provider 失败: %v", err)
	}

	// 使用 Provider
	_ = hidProvider
	_ = adbProvider
}

// ExampleAdvancedGestures 展示高级手势
func Example_advancedGestures() {
	screenState := &screen.ScreenState{}
	factory := mnk.NewProviderFactory(screenState)
	provider, _ := factory.CreateHIDProvider("/dev/hidg0", "/dev/hidg1", "", true, "qwerty")

	// 1. L 型手势（向下再向右）
	provider.Drag([][2]float64{
		{500, 200}, // 起点
		{500, 800}, // 向下
		{900, 800}, // 向右
	}, "left")

	// 2. Z 型手势
	provider.Drag([][2]float64{
		{100, 200},  // 左上
		{900, 200},  // 右上
		{100, 800},  // 左下
		{900, 800},  // 右下
	}, "left")

	// 3. 圆形手势（8 点近似）
	centerX := 500.0
	centerY := 500.0
	radius := 200.0
	steps := 8
	path := make([][2]float64, steps+1)
	for i := 0; i <= steps; i++ {
		angle := float64(i) * 2.0 * 3.14159 / float64(steps)
		x := centerX + radius*cosApprox(angle)
		y := centerY + radius*sinApprox(angle)
		path[i] = [2]float64{x, y}
	}
	provider.Drag(path, "left")

	// 4. 星形手势（5 个点）
	provider.Drag([][2]float64{
		{500, 200},  // 顶点
		{300, 700},  // 左下
		{800, 350},  // 右中
		{200, 350},  // 左中
		{700, 700},  // 右下
		{500, 200},  // 回到顶点
	}, "left")
}

// 简单的三角函数近似
func cosApprox(angle float64) float64 {
	// 简化实现
	return 1.0
}

func sinApprox(angle float64) float64 {
	// 简化实现
	return 0.0
}

// ExampleErrorHandling 展示错误处理
func Example_errorHandling() {
	screenState := &screen.ScreenState{}
	factory := mnk.NewProviderFactory(screenState)
	provider, err := factory.CreateHIDProvider("/dev/hidg0", "/dev/hidg1", "", true, "qwerty")
	if err != nil {
		log.Fatalf("创建 Provider 失败: %v", err)
	}

	// 处理坐标超出范围
	err = provider.Click(1500, 500, "left", 0) // 超出 0-1000 范围
	if err != nil {
		log.Printf("坐标错误: %v", err)
		// 应该返回类似 "coordinates must be in range 0-1000" 的错误
	}

	// 处理空路径
	err = provider.Drag([][2]float64{}, "left")
	if err != nil {
		log.Printf("路径错误: %v", err)
		// 应该返回 "path must contain at least 2 points" 的错误
	}

	// 处理无效按键
	err = provider.Keypress([]string{"invalid_key"})
	if err != nil {
		log.Printf("按键错误: %v", err)
		// 应该返回 "unknown key" 的错误
	}

	// 处理 ADB 特定错误
	adbProvider, _ := factory.CreateADBProvider()
	err = adbProvider.Move(500, 500) // ADB 不支持 Move
	if err != nil {
		log.Printf("不支持的操作: %v", err)
		// 应该返回 "move is unsupported on adb" 的错误
	}
}

// ExamplePerformanceOptimization 展示性能优化技巧
func Example_performanceOptimization() {
	screenState := &screen.ScreenState{}
	factory := mnk.NewProviderFactory(screenState)
	provider, _ := factory.CreateHIDProvider("/dev/hidg0", "/dev/hidg1", "", true, "qwerty")

	// 1. 批量操作 - 使用多点路径而不是多次调用
	// 不好的做法：
	// provider.Drag([][2]float64{{100,500},{300,500}}, "left")
	// provider.Drag([][2]float64{{300,500},{500,500}}, "left")
	// provider.Drag([][2]float64{{500,500},{900,500}}, "left")

	// 好的做法：
	provider.Drag([][2]float64{
		{100, 500},
		{300, 500},
		{500, 500},
		{900, 500},
	}, "left")

	// 2. 复用 Provider 实例
	// 不要在每次调用时都创建新的 Provider

	// 3. 预设屏幕尺寸
	// 在应用启动时设置一次，避免重复查询
	screenState.Update(1920, 1080)
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{
		Valid:  true,
		X:      0,
		Y:      0,
		Width:  1920,
		Height: 1080,
	})
}
