package skills

import (
	"math/rand"
	"testing"
	"time"
)

func TestRecharge(t *testing.T) {
	now := time.Now()
	s := Skill{
		Recharge: 8,
	}
	Skills.Set("per", s)
	u := Use{
		user:  "a",
		skill: "per",
		at:    now,
	}
	if u.Recharge() != now.Add(time.Second*8) {
		t.Errorf("got %v, want %v", u.Recharge(), time.Second*80)
	}
	if r := skillUses.recharge(u); r != 1 {
		t.Errorf("got %v, want 1", r)
	}
	if r := skillUses.recharge(u); r != 0 {
		t.Errorf("got %v, want 0", r)
	}
	u.at = u.at.Add(time.Second)
	if r := skillUses.recharge(u); r != 0.5 {
		t.Errorf("got %v, want 0.5", r)
	}
	u.at = u.at.Add(time.Second * 2)
	if r := skillUses.recharge(u); r != 0.75 {
		t.Errorf("got %v, want 0.75", r)
	}
	u.at = u.at.Add(time.Second * 3)
	if r := skillUses.recharge(u); r != 0.875 {
		t.Errorf("got %v, want 0.875", r)
	}
}

func TestDuration(t *testing.T) {
	now := time.Now()
	s := Skill{
		Duration: 60,
	}
	Skills.Set("per", s)
	u := Use{
		user:  "a",
		skill: "per",
		at:    now,
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
	u.at = u.at.Add(s.Duration.Duration() * 3)
	if f1 := u.RNG("b").Float64(); f0 == f1 {
		t.Errorf("got %v, wanted something else", f0)
	}
	testAt := func(f float64) float64 {
		same := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.at = time.Unix(0, rand.Int63())
			f0 = u.RNG("b").Float64()
			u.at = u.at.Add(time.Duration(float64(s.Duration.Nanoseconds()) * f))
			if f1 := u.RNG("b").Float64(); f1 == f0 {
				same++
			}
		}
		return float64(same) / float64(count)
	}
	if atHalf := testAt(1); atHalf > 0.55 || atHalf < 0.45 {
		t.Errorf("wanted 0.5, got %v", atHalf)
	}
	// for f := 0.0; f < 3.0; f += 0.05 {
	// 	rate := testAt(f)
	// 	width := 30 * rate
	// 	marks := ""
	// 	for i := 0.0; i < width; i++ {
	// 		marks += "#"
	// 	}
	// 	log.Printf("%.2f: %.2f: %s\n", f, testAt(f), marks)
	// }
}

func TestLevel(t *testing.T) {
	now := time.Now()
	u := Application{
		use: Use{
			user:  "a",
			skill: "str",
			at:    now,
		},
		target:    "b",
		level:     10,
		challenge: 20,
	}
	testAt := func(delta float32) float64 {
		success := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.use.at = time.Now()
			u.challenge = u.level + delta
			if u.check() {
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
