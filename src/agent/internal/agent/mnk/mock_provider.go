package mnk

// MockProvider is a recording Provider for tests and adapter contract checks.
type MockProvider struct {
	clicks       []MockClick
	doubleClicks []MockDoubleClick
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

func (m *MockProvider) Click(x, y float64, button string, holdMs int) error {
	m.clicks = append(m.clicks, MockClick{X: x, Y: y, Button: button, HoldMs: holdMs})
	return nil
}

func (m *MockProvider) DoubleClick(x, y float64, button string) error {
	m.doubleClicks = append(m.doubleClicks, MockDoubleClick{X: x, Y: y, Button: button})
	return nil
}

func (m *MockProvider) Drag(path [][2]float64, button string) error {
	m.drags = append(m.drags, MockDrag{Path: path, Button: button})
	return nil
}

func (m *MockProvider) Keypress(keys []string) error {
	m.keypresses = append(m.keypresses, MockKeypress{Keys: keys})
	return nil
}

func (m *MockProvider) Move(x, y float64) error {
	m.moves = append(m.moves, MockMove{X: x, Y: y})
	return nil
}

func (m *MockProvider) Scroll(scrollX, scrollY int) error {
	m.scrolls = append(m.scrolls, MockScroll{ScrollX: scrollX, ScrollY: scrollY})
	return nil
}

func (m *MockProvider) Reset() {
	m.clicks = nil
	m.doubleClicks = nil
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
