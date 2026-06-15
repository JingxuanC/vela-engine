package visibility

import (
	"testing"
)

func TestCalculateOverall(t *testing.T) {
	tests := []struct {
		name string
		ucp  float64
		geo  float64
		seo  float64
		want float64
	}{
		{"all perfect", 100, 100, 100, 100},
		{"all zero", 0, 0, 0, 0},
		{"weighted typical", 65, 75, 68, 69.3}, // 65*0.40 + 75*0.35 + 68*0.25 = 26+26.25+17 = 69.35
		{"ucp dominant", 100, 0, 0, 40},
		{"geo dominant", 0, 100, 0, 35},
		{"seo dominant", 0, 0, 100, 25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateOverall(tt.ucp, tt.geo, tt.seo)
			if got != tt.want {
				t.Errorf("CalculateOverall(%.1f, %.1f, %.1f) = %.1f, want %.1f",
					tt.ucp, tt.geo, tt.seo, got, tt.want)
			}
		})
	}
}

func TestCalculateSubScore(t *testing.T) {
	checks := []CheckItem{
		{Name: "a", Weight: 0.5, Score: 80, Passed: true, Message: "ok"},
		{Name: "b", Weight: 0.3, Score: 100, Passed: true, Message: "ok"},
		{Name: "c", Weight: 0.2, Score: 50, Passed: false, Message: "bad"},
	}
	// 80*0.5 + 100*0.3 + 50*0.2 = 40+30+10 = 80
	result := CalculateSubScore(checks)
	if result.Score != 80 {
		t.Errorf("CalculateSubScore = %.1f, want 80", result.Score)
	}
}

func TestCalculateSubScore_Normalized(t *testing.T) {
	// Weights don't sum to 1.0 — should normalize
	checks := []CheckItem{
		{Name: "a", Weight: 0.4, Score: 100},
		{Name: "b", Weight: 0.4, Score: 50},
	}
	// 100*0.4 + 50*0.4 = 40+20 = 60 / 0.8 = 75
	result := CalculateSubScore(checks)
	if result.Score != 75 {
		t.Errorf("CalculateSubScore normalized = %.1f, want 75", result.Score)
	}
}

func TestCalculateSubScore_Empty(t *testing.T) {
	result := CalculateSubScore(nil)
	if result.Score != 0 {
		t.Errorf("empty checks should score 0, got %.1f", result.Score)
	}
}

func TestBuildScore(t *testing.T) {
	ucp := SubScore{Score: 65, Checks: []CheckItem{}}
	geo := SubScore{Score: 75, Checks: []CheckItem{}}
	seo := SubScore{Score: 68, Checks: []CheckItem{}}

	score := BuildScore(ucp, geo, seo, "")
	if score.Overall != 69.3 {
		t.Errorf("BuildScore overall = %.1f, want 69.3", score.Overall)
	}
	if score.Grade != "good" {
		t.Errorf("grade should be 'good', got '%s'", score.Grade)
	}
}

func TestGrade(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{95, "excellent"},
		{80, "excellent"},
		{60, "good"},
		{79, "good"},
		{40, "fair"},
		{59, "fair"},
		{0, "poor"},
		{39, "poor"},
	}
	for _, tt := range tests {
		got := Grade(tt.score)
		if got != tt.want {
			t.Errorf("Grade(%.0f) = %s, want %s", tt.score, got, tt.want)
		}
	}
}

func TestCollectIssues(t *testing.T) {
	ucp := SubScore{Checks: []CheckItem{
		{Name: "title", Weight: 0.20, Score: 50, Passed: false, Message: "bad title"},
		{Name: "price", Weight: 0.10, Score: 100, Passed: true, Message: "ok"},
	}}
	geo := SubScore{Checks: []CheckItem{
		{Name: "llms", Weight: 0.05, Score: 30, Passed: false, Message: "no llms"},
	}}
	seo := SubScore{Checks: []CheckItem{}}

	issues := CollectIssues(ucp, geo, seo, "")

	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	// Critical (weight 0.20) should come before info (weight 0.05)
	if issues[0].Severity != SeverityCritical {
		t.Errorf("first issue should be critical, got %s", issues[0].Severity)
	}
	if issues[1].Severity != SeverityInfo {
		t.Errorf("second issue should be info, got %s", issues[1].Severity)
	}
}
