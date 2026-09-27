package clock

import (
	"math"
	"testing"
	"time"
)

func TestDeadlineAfterRejectsNonPositiveTTLAndOverflow(t *testing.T) {
	if deadline, ok := DeadlineAfter(time.Second, time.Minute); !ok || deadline != time.Second+time.Minute {
		t.Fatalf("DeadlineAfter(1s, 1m) = (%v, %v)", deadline, ok)
	}
	for name, input := range map[string][2]time.Duration{
		"zero ttl":     {time.Second, 0},
		"negative ttl": {time.Second, -time.Second},
		"overflow":     {time.Duration(math.MaxInt64), time.Nanosecond},
	} {
		if _, ok := DeadlineAfter(input[0], input[1]); ok {
			t.Fatalf("%s: DeadlineAfter accepted %v + %v", name, input[0], input[1])
		}
	}
}

func TestSystemReadsUTCWallAndNonDecreasingMono(t *testing.T) {
	now := System()
	first, second := now(), now()
	if first.Wall.Location() != time.UTC || first.Mono < 0 || second.Mono < first.Mono {
		t.Fatalf("readings = %+v, %+v", first, second)
	}
}
