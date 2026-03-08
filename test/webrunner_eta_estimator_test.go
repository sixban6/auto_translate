package test

import (
	"testing"
	"time"

	"auto_translate/pkg/webtask"
)

func TestETAEstimator_WarmupAndCompletion(t *testing.T) {
	estimator := webtask.NewETAEstimator(0.25, 5)

	eta := estimator.Estimate(3, 100, 10*time.Second)
	if eta != -1 {
		t.Fatalf("expected warmup eta to be -1, got %d", eta)
	}

	eta = estimator.Estimate(100, 100, 120*time.Second)
	if eta != 0 {
		t.Fatalf("expected completed eta to be 0, got %d", eta)
	}
}

func TestETAEstimator_NonNegativeAndTrendDown(t *testing.T) {
	estimator := webtask.NewETAEstimator(0.25, 5)

	e1 := estimator.Estimate(5, 100, 5*time.Second)
	e2 := estimator.Estimate(20, 100, 20*time.Second)
	e3 := estimator.Estimate(60, 100, 60*time.Second)

	if e1 < 0 || e2 < 0 || e3 < 0 {
		t.Fatalf("eta should not be negative: e1=%d e2=%d e3=%d", e1, e2, e3)
	}
	if e3 > e2 || e2 > e1 {
		t.Fatalf("eta should trend down with progress: e1=%d e2=%d e3=%d", e1, e2, e3)
	}
}
