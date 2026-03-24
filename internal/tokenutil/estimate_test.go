package tokenutil

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hi", 1},
		{"hello world", 2},
		{strings.Repeat("a", 400), 100},
		{strings.Repeat("x", 4000), 1000},
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%d chars) = %d, want %d", len(tt.input), got, tt.want)
		}
	}
}

func TestCharsForTokens(t *testing.T) {
	if got := CharsForTokens(100); got != 400 {
		t.Errorf("CharsForTokens(100) = %d, want 400", got)
	}
}

func TestBudgetConstants(t *testing.T) {
	if ConversationBudgetPct+MemoryBudgetPct != 100 {
		t.Errorf("budget percentages should sum to 100, got %d", ConversationBudgetPct+MemoryBudgetPct)
	}
}
