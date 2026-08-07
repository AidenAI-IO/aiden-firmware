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

func TestHasProviderNormalizesName(t *testing.T) {
	old := factories
	t.Cleanup(func() { factories = old })
	factories = map[string]Factory{"alpha": func(ProviderConfig) (TTSProvider, error) { return nil, nil }}

	if !HasProvider(" Alpha ") {
		t.Fatal("HasProvider() = false, want true for normalized registered name")
	}
	if HasProvider("missing") {
		t.Fatal("HasProvider() = true for unregistered name")
	}
}

func TestRegisterPanicsOnNilFactory(t *testing.T) {
	old := factories
	t.Cleanup(func() { factories = old })
	factories = map[string]Factory{}

	defer func() {
		if recover() == nil {
			t.Fatal("Register() did not panic for nil factory")
		}
	}()
	Register("nil-provider", nil)
}
