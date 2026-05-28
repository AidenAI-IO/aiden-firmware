package tts

import "testing"

func TestAvailableProvidersSorted(t *testing.T) {
	old := factories
	t.Cleanup(func() { factories = old })
	factories = map[string]Factory{
		"zeta":  nil,
		"alpha": nil,
	}

	got := AvailableProviders()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("AvailableProviders() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AvailableProviders() = %#v, want %#v", got, want)
		}
	}
}
