package skills

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestReuse(t *testing.T) {
	now := time.Now()
	s := Skill{
		Recharge: 8,
		Reuse:    0.8,
	}
	Skills.Set("str", s)
	u := Use{
		User:  "a",
		Skill: "str",
		At:    now,
	}
	for i, ex := range []struct {
		delay time.Duration
		reuse float32
	}{
		{
			delay: 0,
			reuse: 1.0,
		},
		{
			delay: time.Second * 4,
			reuse: 0.81171876,
		},
		{
			delay: time.Second * 4,
			reuse: 0.66991943,
		},
		{
			delay: time.Second * 5,
			reuse: 0.5921309,
		},
		{
			delay: time.Second * 5,
			reuse: 0.53743577,
		},
		{
			delay: time.Second * 6,
			reuse: 0.5702347,
		},
		{
			delay: time.Second * 6,
			reuse: 0.5900166,
		},
		{
			delay: time.Second * 7,
			reuse: 0.7339442,
		},
		{
			delay: time.Second * 7,
			reuse: 0.791965,
		},
		{
			delay: time.Second * 2,
			reuse: 0.63786614,
		},
		{
			delay: time.Second * 7,
			reuse: 0.75323355,
		},
		{
			delay: time.Second * 7,
			reuse: 0.799741,
		},
		{
			delay: time.Second * 7,
			reuse: 0.8184894,
		},
		{
			delay: time.Second * 7,
			reuse: 0.8260473,
		},
		{
			delay: time.Second * 8,
			reuse: 0.9986751,
		},
	} {
		u.At = u.At.Add(ex.delay)
		if r := skillUses.reuse(u); r != ex.reuse {
			t.Errorf("example %v, after %v: got %v, want %v", i, u.At.Sub(now), r, ex.reuse)
		}
	}
}

func assert(t *testing.T, f1, f2 float32) {
	t.Helper()
	if math.Abs(float64(f1)-float64(f2)) > 0.01 {
		t.Errorf("got %v, want %v", f1, f2)
	}
}

func TestRecharge(t *testing.T) {
	now := time.Now()
	s := Skill{
		Recharge: 100,
	}
	Skills.Set("per", s)
	u := Use{
		User:  "a",
		Skill: "per",
		At:    now,
	}
	if u.RechargedAt() != now.Add(time.Second*100) {
		t.Errorf("got %v, want %v", u.RechargedAt(), now.Add(time.Second*100))
	}
	if r := skillUses.reuse(u); r != 1 {
		t.Errorf("got %v, want 1", r)
	}
	if r := skillUses.reuse(u); r != 0 {
		t.Errorf("got %v, want 0", r)
	}
	u.At = u.At.Add(time.Second * 40)
	assert(t, 0.032, skillUses.reuse(u))
	u.At = u.At.Add(time.Second * 60)
	assert(t, 0.105, skillUses.reuse(u))
	u.At = u.At.Add(time.Second * 80)
	assert(t, 0.326, skillUses.reuse(u))
	u.At = u.At.Add(time.Second * 100)
	assert(t, 0.996, skillUses.reuse(u))
}

func TestDuration(t *testing.T) {
	now := time.Now()
	s := Skill{
		Duration: 60,
	}
	Skills.Set("per", s)
	u := Use{
		User:  "a",
		Skill: "per",
		At:    now,
	}
	// Reference
	f0 := u.RNG("b").Float64()
	if f1 := u.RNG("b").Float64(); f0 != f1 {
		t.Errorf("got %v, wanted %v", f1, f0)
	}
	if f1 := u.RNG("b").Float64(); f0 != f1 {
		t.Errorf("got %v, wanted %v", f1, f0)
	}
	// Different target
	if f1 := u.RNG("c").Float64(); f0 == f1 {
		t.Errorf("got %v, wanted something else", f0)
	}
	// After duration timeout
	u.At = u.At.Add(s.Duration.Duration() * 3)
	if f1 := u.RNG("b").Float64(); f0 == f1 {
		t.Errorf("got %v, wanted something else", f0)
	}
	testAt := func(f float64) float64 {
		same := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.At = time.Unix(0, rand.Int63())
			f0 = u.RNG("b").Float64()
			u.At = u.At.Add(time.Duration(float64(s.Duration.Nanoseconds()) * f))
			if f1 := u.RNG("b").Float64(); f1 == f0 {
				same++
			}
		}
		return float64(same) / float64(count)
	}
	wantedRatios := map[float64]float64{
		0.0: 1.0,
		0.5: 0.7,
		1.0: 0.5,
		1.5: 0.35,
		2.0: 0.17,
		2.5: 0.04,
		3.0: 0.0,
	}
	for durRatio, wantSucRatio := range wantedRatios {
		if gotSucRatio := testAt(durRatio); math.Abs(gotSucRatio-wantSucRatio) > 0.05 {
			t.Errorf("wanted %.2f%% success at %.2f%% recharge, but got %.2f", wantSucRatio, durRatio, gotSucRatio)
		}
	}
}

func TestLevel(t *testing.T) {
	now := time.Now()
	u := Application{
		Use: Use{
			User:  "a",
			Skill: "agi",
			At:    now,
		},
		Target:    "b",
		Level:     10,
		Challenge: 20,
	}
	testAt := func(delta float32) float64 {
		success := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.Use.At = time.Now()
			u.Challenge = u.Level + delta
			if u.Check() {
				success++
			}
		}
		return float64(success) / float64(count)
	}
	if at := testAt(10); at < 0.08 || at > 0.12 {
		t.Errorf("wanted 0.1, got %v", at)
	}
	if at := testAt(-10); at < 0.88 || at > 0.92 {
		t.Errorf("wanted 0.9, got %v", at)
	}
}
