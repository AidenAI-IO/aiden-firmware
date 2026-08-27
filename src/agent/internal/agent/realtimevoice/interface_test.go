package realtimevoice

import "testing"

func TestProviderInterfaceSeparatesCoreAndTextCapabilities(t *testing.T) {
	var _ Session = (*spekoSession)(nil)
	if _, ok := any((*spekoSession)(nil)).(TextSession); ok {
		t.Fatal("Speko S2S must not advertise undocumented text injection")
	}
	var _ TextSession = (*qwenSession)(nil)
}
