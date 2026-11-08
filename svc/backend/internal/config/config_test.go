package config

import "testing"

func TestLoadFDRQDefaultAndOverride(t *testing.T) {
	t.Setenv("FDR_Q", "")
	if got := Load().FDRQ; got != 0.1 {
		t.Fatalf("default FDRQ = %v, want 0.1", got)
	}

	t.Setenv("FDR_Q", "0.2")
	if got := Load().FDRQ; got != 0.2 {
		t.Fatalf("overridden FDRQ = %v, want 0.2", got)
	}
}
