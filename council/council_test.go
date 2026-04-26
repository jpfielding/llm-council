package council

import "testing"

func TestParseRankingsStrict(t *testing.T) {
	text := "1. Response C\n2. Response A\n3. Response B\n4. Response D"
	got := parseRankings(text, []string{"A", "B", "C", "D"})
	want := map[string]int{"A": 2, "B": 3, "C": 1, "D": 4}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("label %s: got %d want %d", k, got[k], v)
		}
	}
}

func TestParseRankingsVariations(t *testing.T) {
	cases := []struct {
		name string
		text string
		want map[string]int
	}{
		{
			name: "parenthesis form",
			text: "1) Response A\n2) Response B",
			want: map[string]int{"A": 1, "B": 2},
		},
		{
			name: "bold markdown",
			text: "1. **Response A**\n2. **Response B**",
			want: map[string]int{"A": 1, "B": 2},
		},
		{
			name: "bare letter",
			text: "1. A\n2. B",
			want: map[string]int{"A": 1, "B": 2},
		},
		{
			name: "lowercase response",
			text: "1. response a\n2. response b",
			want: map[string]int{"A": 1, "B": 2},
		},
		{
			name: "with surrounding narrative",
			text: "After evaluation, my final ranking:\n\n1. Response B\n2. Response A\n\nThis ranking reflects clarity.",
			want: map[string]int{"A": 2, "B": 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRankings(tc.text, []string{"A", "B"})
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("label %s: got %d want %d (full=%v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestParseRankingsRejectsDuplicates(t *testing.T) {
	// Duplicate label: first occurrence wins, second ignored
	text := "1. Response A\n2. Response A\n3. Response B"
	got := parseRankings(text, []string{"A", "B"})
	if got["A"] != 1 {
		t.Errorf("A: got %d want 1", got["A"])
	}
	if got["B"] != 3 {
		t.Errorf("B: got %d want 3", got["B"])
	}
}

func TestParseRankingsRejectsInvalidLabels(t *testing.T) {
	// Z is not in the label set; should be ignored
	text := "1. Response Z\n2. Response A"
	got := parseRankings(text, []string{"A", "B"})
	if got["A"] != 2 {
		t.Errorf("A: got %d want 2", got["A"])
	}
	if _, ok := got["Z"]; ok {
		t.Errorf("Z should not be in result map")
	}
}

func TestParseRankingsUnrankedGetsWorstPosition(t *testing.T) {
	// Only A ranked, B should default to n+1 = 3
	text := "1. Response A"
	got := parseRankings(text, []string{"A", "B"})
	if got["A"] != 1 {
		t.Errorf("A: got %d want 1", got["A"])
	}
	if got["B"] != 3 {
		t.Errorf("B: got %d want 3 (unranked default)", got["B"])
	}
}

func TestParseRankingsEmpty(t *testing.T) {
	got := parseRankings("", []string{"A", "B"})
	// Both unranked -> both get n+1 = 3
	if got["A"] != 3 || got["B"] != 3 {
		t.Errorf("all should be 3 (unranked), got %v", got)
	}
}

func TestComputeAggregateScores(t *testing.T) {
	// Three models rank A, B, C:
	// Model 1: A=1, B=2, C=3
	// Model 2: A=2, B=1, C=3
	// Model 3: A=1, B=3, C=2
	// Aggregate: A=(1+2+1)/3=1.333, B=(2+1+3)/3=2.0, C=(3+3+2)/3=2.666
	all := []map[string]int{
		{"A": 1, "B": 2, "C": 3},
		{"A": 2, "B": 1, "C": 3},
		{"A": 1, "B": 3, "C": 2},
	}
	got := computeAggregateScores(all, []string{"A", "B", "C"})
	checkApprox := func(label string, want float64) {
		t.Helper()
		if diff := got[label] - want; diff > 0.01 || diff < -0.01 {
			t.Errorf("label %s: got %.4f want %.4f", label, got[label], want)
		}
	}
	checkApprox("A", 4.0/3.0)
	checkApprox("B", 2.0)
	checkApprox("C", 8.0/3.0)
}

func TestComputeAggregateScoresAllNil(t *testing.T) {
	// All models failed. All labels should get n+1 (worst).
	got := computeAggregateScores([]map[string]int{nil, nil}, []string{"A", "B"})
	if got["A"] != 3 || got["B"] != 3 {
		t.Errorf("expected all 3 (unranked default), got %v", got)
	}
}
