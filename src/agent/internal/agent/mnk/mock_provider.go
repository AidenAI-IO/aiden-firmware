package mnk

import "context"

// MockProvider is a recording Provider for tests and adapter contract checks.
type MockProvider struct {
	clicks       []MockClick
	doubleClicks []MockDoubleClick
	swipes       []MockDrag
	drags        []MockDrag
	keypresses   []MockKeypress
	moves        []MockMove
	scrolls      []MockScroll
}

type MockClick struct {
	X, Y   float64
	Button string
	HoldMs int
}

type MockDoubleClick struct {
	X, Y   float64
	Button string
}

type MockDrag struct {
	Path   [][2]float64
	Button string
}

type MockKeypress struct {
	Keys []string
}

type MockMove struct {
	X, Y float64
}

type MockScroll struct {
	ScrollX, ScrollY int
}

func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

func (m *MockProvider) Click(ctx context.Context, x, y float64, button string, holdMs int) error {
	_ = ctx
	m.clicks = append(m.clicks, MockClick{X: x, Y: y, Button: button, HoldMs: holdMs})
	return nil
}

func (m *MockProvider) DoubleClick(ctx context.Context, x, y float64, button string) error {
	_ = ctx
	m.doubleClicks = append(m.doubleClicks, MockDoubleClick{X: x, Y: y, Button: button})
	return nil
}

func (m *MockProvider) Swipe(ctx context.Context, path [][2]float64, button string) error {
	_ = ctx
	m.swipes = append(m.swipes, MockDrag{Path: path, Button: button})
	return nil
}

func (m *MockProvider) Drag(ctx context.Context, path [][2]float64, button string) error {
	_ = ctx
	m.drags = append(m.drags, MockDrag{Path: path, Button: button})
	return nil
}

func (m *MockProvider) Keypress(ctx context.Context, keys []string) error {
	_ = ctx
	m.keypresses = append(m.keypresses, MockKeypress{Keys: keys})
	return nil
}

func (m *MockProvider) Move(ctx context.Context, x, y float64) error {
	_ = ctx
	m.moves = append(m.moves, MockMove{X: x, Y: y})
	return nil
}

func (m *MockProvider) Scroll(ctx context.Context, scrollX, scrollY int) error {
	_ = ctx
	m.scrolls = append(m.scrolls, MockScroll{ScrollX: scrollX, ScrollY: scrollY})
	return nil
}

func (m *MockProvider) Reset() {
	m.clicks = nil
	m.doubleClicks = nil
	m.swipes = nil
	m.drags = nil
	m.keypresses = nil
	m.moves = nil
	m.scrolls = nil
}

func (m *MockProvider) KeypressCalls() []MockKeypress {
	if m == nil {
		return nil
	}
	out := make([]MockKeypress, len(m.keypresses))
	copy(out, m.keypresses)
	return out
}
