package agent

import "testing"

func TestMatchesAnyRelaxedName(t *testing.T) {
	tests := []struct {
		name       string
		query      []string
		candidates []string
		want       bool
	}{
		{"exact match", []string{"QA Notes"}, []string{"QA Notes"}, true},
		{"case insensitive", []string{"qa notes"}, []string{"QA Notes"}, true},
		{"substring via token", []string{"Notes"}, []string{"QA Notes"}, true},
		{"query is longer", []string{"Notes app"}, []string{"QA Notes"}, true},
		{"no overlap", []string{"Calendar"}, []string{"QA Notes"}, false},
		{"generic token only", []string{"app"}, []string{"QA Notes"}, false},
		{"empty query", []string{}, []string{"QA Notes"}, true},
		{"empty candidate", []string{"Notes"}, []string{""}, false},
		{"multi candidate", []string{"Notes"}, []string{"Calendar", "QA Notes"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesAnyRelaxedName(tt.query, tt.candidates)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
