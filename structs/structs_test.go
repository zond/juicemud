package structs

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func assertClose[T float64 | float32 | int | time.Duration](t *testing.T, f1, f2, delta T) {
	t.Helper()
	if math.Abs(float64(f1)-float64(f2)) > float64(delta) {
		t.Errorf("got %v, want %v", f1, f2)
	}
}

func TestLevel(t *testing.T) {
	u := SkillUse{
		User: "a",
		Skill: &Skill{
			Practical: 10,
		},
		Target: "b",
	}
	testAt := func(delta float64) float64 {
		success := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.At = time.Unix(0, rand.Int64())
			if u.Check(float64(u.Skill.Practical) + delta) {
				success++
			}
		}
		return float64(success) / float64(count)
	}
	assertClose(t, testAt(20), 0.01, 0.002)
	assertClose(t, testAt(10), 0.1, 0.02)
	assertClose(t, testAt(0), 0.5, 0.02)
	assertClose(t, testAt(-10), 0.9, 0.02)
	assertClose(t, testAt(-20), 0.99, 0.002)
}

func TestRechargeWithoutReuse(t *testing.T) {
	u := SkillUse{
		User: "a",
		Skill: &Skill{
			Name:      "TestRechargeWithoutReuse",
			Practical: 10,
		},
		Target: "b",
	}
	recharge := time.Minute
	SkillConfigs.Set("TestRechargeWithoutReuse", SkillConfig{
		Recharge: Duration(recharge),
	})
	testAt := func(multiple float64) float64 {
		success := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.Skill.LastUsedAt = Stamp(time.Unix(0, rand.Int64())).Uint64()
			u.At = Timestamp(u.Skill.LastUsedAt).Time().Add(time.Duration(float64(recharge) * multiple))
			if u.Check(u.Skill.Effective(Stamp(u.At))) {
				success++
			}
		}
		return float64(success) / float64(count)
	}
	assertClose(t, testAt(0.0), 0.0, 0.02)
	assertClose(t, testAt(1.0), 0.5, 0.02)
	assertClose(t, testAt(7.0/8), 0.25, 0.02)
	assertClose(t, testAt(6.0/8), 0.125, 0.02)
	assertClose(t, testAt(5.0/8), 0.125/2, 0.02)
}

func TestForget(t *testing.T) {
	now := time.Time{}
	s := &Skill{
		Name:        "TestForget",
		Practical:   20,
		Theoretical: 20,
		LastUsedAt:  Stamp(now).Uint64(),
	}
	forget := time.Hour
	SkillConfigs.Set("TestForget", SkillConfig{
		Forget: Duration(forget),
	})
	assertClose(t, s.Effective(Stamp(now)), 20, 0.02)
	assertClose(t, s.Effective(Stamp(now.Add(forget/8))), 15, 0.02)
	assertClose(t, s.Effective(Stamp(now.Add(2*forget/8))), 12.5, 0.02)
	assertClose(t, s.Effective(Stamp(now.Add(3*forget/8))), 11.25, 0.02)
	assertClose(t, s.Effective(Stamp(now.Add(9*forget/8))), 10, 0.02)

	SkillUse{
		User:   "a",
		Skill:  s,
		Target: "b",
		At:     now.Add(forget / 8),
	}.Check(10.0)
	assertClose(t, s.Effective(Stamp(now.Add(forget/8))), 15, 0.02)
	assertClose(t, s.Practical, 15, 0.02)
}

func TestLearn(t *testing.T) {
	now := time.Time{}
	s := &Skill{
		Name:       "TestLearn",
		LastUsedAt: Stamp(now).Uint64(),
	}
	recharge := 6 * time.Minute
	SkillConfigs.Set("TestLearn", SkillConfig{
		Recharge: Duration(recharge),
	})
	timeToTen := func(multiple float64) time.Duration {
		step := time.Duration(multiple * float64(recharge))
		dur := time.Duration(0)
		for s.Practical < 10 {
			at := Timestamp(s.LastUsedAt).Time().Add(step)
			SkillUse{
				User:   "a",
				Skill:  s,
				Target: "b",
				At:     at,
			}.Check(s.Effective(Stamp(at)))
			dur += step
		}
		return dur
	}
	assertClose(t, timeToTen(1.0), 1000*time.Hour, time.Hour)
	s.Practical = 0
	s.Theoretical = 0
	assertClose(t, timeToTen(0.5), 8510*time.Hour, time.Hour)
	s.Practical = 0
	s.Theoretical = 0
	assertClose(t, timeToTen(2.0), 1992*time.Hour, time.Hour)
	s.Practical = 5
	s.Theoretical = 5
	assertClose(t, timeToTen(1.0), 970*time.Hour, time.Hour)
	s.Practical = 5
	s.Theoretical = 10
	assertClose(t, timeToTen(1.0), 320*time.Hour, time.Hour)
}

func TestRechargeWithReuse(t *testing.T) {
	u := SkillUse{
		User: "a",
		Skill: &Skill{
			Name:      "TestRechargeWithReuse",
			Practical: 10,
		},
		Target: "b",
	}
	recharge := time.Minute
	SkillConfigs.Set("TestRechargeWithReuse", SkillConfig{
		Recharge: Duration(recharge),
		Reuse:    0.5,
	})
	testAt := func(multiple float64) float64 {
		count := 10000
		success := 0
		for i := 0; i < count; i++ {
			u.Skill.LastBase = 0
			u.Skill.LastUsedAt = 0
			u.At = time.Unix(0, rand.Int64())
			u.Check(0.0)
			u.At = u.At.Add(time.Duration(float64(recharge) * multiple))
			if u.Check(u.Skill.Effective(Stamp(u.At))) {
				success++
			}
		}
		return float64(success) / float64(count)
	}
	assertClose(t, testAt(0.0), 0.25, 0.02)
	assertClose(t, testAt(1.0), 0.5, 0.02)
	assertClose(t, testAt(7.0/8), 0.75*0.5, 0.02)
	assertClose(t, testAt(6.0/8), 0.625*0.5, 0.02)
}

func TestDuration(t *testing.T) {
	u := SkillUse{
		User: "a",
		Skill: &Skill{
			Name:      "TestDuration",
			Practical: 10,
		},
		Target: "b",
	}
	SkillConfigs.Set("TestDuration", SkillConfig{
		Duration: Duration(time.Minute),
	})
	testAt := func(multiple float64) float64 {
		same := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.At = time.Unix(0, rand.Int64())
			val1 := u.rng().Float64()
			u.At = u.At.Add(time.Duration(float64(time.Minute) * multiple))
			val2 := u.rng().Float64()
			if val1 == val2 {
				same += 1
			}
		}
		return float64(same) / float64(count)
	}
	assertClose(t, testAt(0.0), 1.0, 0.02)
	assertClose(t, testAt(1.0), 0.5, 0.02)
	assertClose(t, testAt(3.0), 0.0, 0.02)
}
