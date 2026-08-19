package agent

import "testing"

func TestMatchesAnyMemoryName(t *testing.T) {
	tests := []struct {
		name       string
		query      []string
		candidates []string
		want       bool
	}{
		{"exact match", []string{"QA Notes"}, []string{"QA Notes"}, true},
		{"case insensitive", []string{"qa notes"}, []string{"QA Notes"}, true},
		{"contained name", []string{"Notes"}, []string{"QA Notes"}, true},
		{"known alias", []string{"Notes app"}, []string{"QA Notes", "Notes app"}, true},
		{"unrecorded rewording", []string{"Notes app"}, []string{"QA Notes"}, false},
		{"no overlap", []string{"Calendar"}, []string{"QA Notes"}, false},
		{"short partial name", []string{"app"}, []string{"QA Notes app"}, false},
		{"empty query", []string{}, []string{"QA Notes"}, true},
		{"empty candidate", []string{"Notes"}, []string{""}, false},
		{"multi candidate", []string{"Notes"}, []string{"Calendar", "QA Notes"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesAnyMemoryName(tt.query, tt.candidates)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
