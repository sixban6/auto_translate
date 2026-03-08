package webtask

import (
	"fmt"
	"math"
	"time"
)

type LogMsg struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	Total      int    `json:"total"`
	Current    int    `json:"current"`
	Status     string `json:"status"`
	ElapsedSec int    `json:"elapsed_sec"`
	EtaSec     int    `json:"eta_sec"`
}

type ETAEstimator struct {
	alpha         float64
	warmupMinimum int
	hasRate       bool
	smoothedRate  float64
}

func NewETAEstimator(alpha float64, warmupMinimum int) *ETAEstimator {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.25
	}
	if warmupMinimum < 1 {
		warmupMinimum = 5
	}
	return &ETAEstimator{
		alpha:         alpha,
		warmupMinimum: warmupMinimum,
	}
}

func (e *ETAEstimator) Estimate(current, total int, elapsed time.Duration) int {
	if total <= 0 {
		return -1
	}
	if current >= total {
		return 0
	}
	elapsedSec := int(elapsed.Seconds())
	if elapsedSec <= 0 || current < e.warmupMinimum {
		return -1
	}
	newRate := float64(current) / float64(elapsedSec)
	if newRate <= 0 {
		return -1
	}
	if !e.hasRate {
		e.smoothedRate = newRate
		e.hasRate = true
	} else {
		e.smoothedRate = e.alpha*newRate + (1-e.alpha)*e.smoothedRate
	}
	if e.smoothedRate <= 0 {
		return -1
	}
	remaining := total - current
	if remaining < 0 {
		remaining = 0
	}
	etaSec := int(math.Ceil(float64(remaining) / e.smoothedRate))
	if etaSec < 0 {
		return 0
	}
	if etaSec > 86400 {
		return 86400
	}
	return etaSec
}

func FormatHHMMSS(totalSec int) string {
	if totalSec < 0 {
		totalSec = 0
	}
	hours := totalSec / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}
