package clock

import (
	"math"
	"time"
)

type Reading struct {
	Wall time.Time
	Mono time.Duration
}

func System() func() Reading {
	origin := time.Now()
	return func() Reading {
		current := time.Now()
		return Reading{Wall: current.UTC(), Mono: current.Sub(origin)}
	}
}

func DeadlineAfter(now, ttl time.Duration) (time.Duration, bool) {
	if ttl <= 0 || now > time.Duration(math.MaxInt64)-ttl {
		return 0, false
	}
	return now + ttl, true
}
