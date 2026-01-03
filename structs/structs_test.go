package structs

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestDescriptionsMatches(t *testing.T) {
	tests := []struct {
		name     string
		descs    Descriptions
		pattern  string
		expected bool
	}{
		// Exact match on full Short description
		{
			name:     "exact match single word",
			descs:    Descriptions{{Short: "torch"}},
			pattern:  "torch",
			expected: true,
		},
		{
			name:     "exact match multi word",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "dusty tome",
			expected: true,
		},
		{
			name:     "no match",
			descs:    Descriptions{{Short: "torch"}},
			pattern:  "book",
			expected: false,
		},

		// Word-based matching
		{
			name:     "match first word",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "dusty",
			expected: true,
		},
		{
			name:     "match second word",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "tome",
			expected: true,
		},
		{
			name:     "match middle word",
			descs:    Descriptions{{Short: "old dusty tome"}},
			pattern:  "dusty",
			expected: true,
		},
		{
			name:     "partial word no match",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "dust",
			expected: false,
		},

		// Glob patterns on full description
		{
			name:     "glob star prefix",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "*tome",
			expected: true,
		},
		{
			name:     "glob star suffix",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "dusty*",
			expected: true,
		},
		{
			name:     "glob star both",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "*tome*",
			expected: true,
		},

		// Glob patterns on individual words
		{
			name:     "glob on word prefix",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "dust*",
			expected: true,
		},
		{
			name:     "glob on word suffix",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "*ome",
			expected: true,
		},
		{
			name:     "glob question mark",
			descs:    Descriptions{{Short: "dusty tome"}},
			pattern:  "tom?",
			expected: true,
		},

		// Multiple descriptions
		{
			name: "match second description",
			descs: Descriptions{
				{Short: "wooden box"},
				{Short: "dusty tome"},
			},
			pattern:  "tome",
			expected: true,
		},

		// Edge cases
		{
			name:     "empty pattern matches empty Short",
			descs:    Descriptions{{Short: ""}},
			pattern:  "",
			expected: true,
		},
		{
			name:     "empty descriptions",
			descs:    Descriptions{},
			pattern:  "torch",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.descs.Matches(tt.pattern)
			if result != tt.expected {
				t.Errorf("Matches(%q) = %v, want %v", tt.pattern, result, tt.expected)
			}
		})
	}
}

func TestDescriptionsLong(t *testing.T) {
	tests := []struct {
		name     string
		descs    Descriptions
		expected string
	}{
		{
			name:     "empty descriptions",
			descs:    Descriptions{},
			expected: "",
		},
		{
			name:     "single description with long",
			descs:    Descriptions{{Long: "A dark room."}},
			expected: "A dark room.",
		},
		{
			name:     "multiple descriptions concatenated",
			descs:    Descriptions{{Long: "A dark room."}, {Long: "Shadows dance on the walls."}},
			expected: "A dark room. Shadows dance on the walls.",
		},
		{
			name:     "skips empty long texts",
			descs:    Descriptions{{Long: "A dark room."}, {Long: ""}, {Long: "It smells musty."}},
			expected: "A dark room. It smells musty.",
		},
		{
			name:     "all empty long texts",
			descs:    Descriptions{{Long: ""}, {Long: ""}},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.descs.Long()
			if result != tt.expected {
				t.Errorf("Long() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestLocationIdentify(t *testing.T) {
	// Helper to create an object with a Short description
	makeObj := func(id, short string) *Object {
		return &Object{
			Unsafe: &ObjectDO{
				Id:           id,
				Descriptions: []Description{{Short: short}},
			},
		}
	}

	// Create a location with multiple objects
	room := makeObj("room1", "dark room")
	torch1 := makeObj("torch1", "burning torch")
	torch2 := makeObj("torch2", "burning torch")
	tome := makeObj("tome1", "dusty tome")
	box := makeObj("box1", "wooden box")

	loc := &Location{
		Container: room,
		Content: Content{
			"torch1": torch1,
			"torch2": torch2,
			"tome1":  tome,
			"box1":   box,
		},
	}

	tests := []struct {
		name        string
		pattern     string
		expectedID  string
		expectError bool
		errorMsg    string
	}{
		// Simple word matching
		{
			name:       "match by single word",
			pattern:    "dusty",
			expectedID: "tome1",
		},
		{
			name:       "match by second word",
			pattern:    "tome",
			expectedID: "tome1",
		},
		{
			name:       "match box by word",
			pattern:    "box",
			expectedID: "box1",
		},
		{
			name:       "match box by wooden",
			pattern:    "wooden",
			expectedID: "box1",
		},

		// Indexed matching with word patterns
		{
			name:       "first torch by index",
			pattern:    "0.torch",
			expectedID: "torch1",
		},
		{
			name:       "second torch by index",
			pattern:    "1.torch",
			expectedID: "torch2",
		},
		{
			name:       "first burning by index",
			pattern:    "0.burning",
			expectedID: "torch1",
		},
		{
			name:       "second burning by index",
			pattern:    "1.burning",
			expectedID: "torch2",
		},

		// Glob patterns
		{
			name:       "glob pattern dust*",
			pattern:    "dust*",
			expectedID: "tome1",
		},
		{
			name:       "glob pattern *orch",
			pattern:    "0.*orch",
			expectedID: "torch1",
		},

		// Exact full description match
		{
			name:       "exact full description",
			pattern:    "dusty tome",
			expectedID: "tome1",
		},
		{
			name:       "exact full description with index",
			pattern:    "0.burning torch",
			expectedID: "torch1",
		},

		// Container (room) matching
		{
			name:       "match room by word",
			pattern:    "room",
			expectedID: "room1",
		},
		{
			name:       "match room by dark",
			pattern:    "dark",
			expectedID: "room1",
		},

		// Error cases
		{
			name:        "no match",
			pattern:     "sword",
			expectError: true,
			errorMsg:    `No "sword" found`,
		},
		{
			name:        "multiple matches without index",
			pattern:     "burning",
			expectError: true,
			errorMsg:    `2 "burning" found, pick one`,
		},
		{
			name:        "index out of range",
			pattern:     "5.torch",
			expectError: true,
			errorMsg:    `Only 2 "torch" found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := loc.Identify(tt.pattern)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorMsg)
				} else if err.Error() != tt.errorMsg {
					t.Errorf("expected error %q, got %q", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				} else if result.GetId() != tt.expectedID {
					t.Errorf("expected object %q, got %q", tt.expectedID, result.GetId())
				}
			}
		})
	}
}

func assertClose[T float64 | float32 | int | time.Duration](t *testing.T, f1, f2, delta T) {
	t.Helper()
	if math.Abs(float64(f1)-float64(f2)) > float64(delta) {
		t.Errorf("got %v, want %v", f1, f2)
	}
}

func TestMulti(t *testing.T) {
	// Test a challenge requiring multiple skills.
	// Uses the same structure as Challenges.Check(): sum individual results, success if > 0.

	cfg := NewServerConfig()

	skills := map[string]*Skill{
		"TestMultiA": {Name: "TestMultiA", Practical: 10},
		"TestMultiB": {Name: "TestMultiB", Practical: 10},
	}

	// Reset all skills to fresh state
	resetSkills := func() {
		for _, skill := range skills {
			skill.Practical = 10
			skill.Theoretical = 10
			skill.LastBase = 1
			skill.LastUsedAt = 0
		}
	}

	// Test combined success rate for a multi-skill challenge.
	// challenges: slice of (skillName, level) pairs
	testMulti := func(challenges []struct {
		skill string
		level float64
	}) float64 {
		success := 0
		count := 10000
		for range count {
			resetSkills()
			at := time.Unix(0, rand.Int64())

			// Sum results from all challenges (mirrors Challenges.Check behavior)
			total := 0.0
			for i, ch := range challenges {
				total += skillUse{
					user:      "tester",
					skill:     skills[ch.skill],
					target:    string(rune('a' + i)), // Different target per challenge for independent RNG
					at:        at,
					challenge: ch.level,
				}.check(cfg, false)
			}

			if total > 0 {
				success++
			}
		}
		return float64(success) / float64(count)
	}

	// Single skill at even odds: 50% success (baseline sanity check)
	assertClose(t, testMulti([]struct {
		skill string
		level float64
	}{
		{"TestMultiA", 10},
	}), 0.5, 0.03)

	// Two skills, both at even odds
	// With unified formula, two 50% checks give ~60% combined success
	// (the score distribution isn't symmetric, slight positive bias when summed)
	assertClose(t, testMulti([]struct {
		skill string
		level float64
	}{
		{"TestMultiA", 10},
		{"TestMultiB", 10},
	}), 0.60, 0.03)

	// Two skills: one easy (level 0 vs skill 10), one hard (level 20 vs skill 10)
	// With unified formula, easy checks contribute more positive magnitude than
	// hard checks contribute negative, so this doesn't perfectly balance.
	assertClose(t, testMulti([]struct {
		skill string
		level float64
	}{
		{"TestMultiA", 0},
		{"TestMultiB", 20},
	}), 0.29, 0.03)

	// Two skills, both easy (level 0 vs skill 10 = 90% each)
	// With unified formula, both contribute positive expected scores, ~98% combined
	assertClose(t, testMulti([]struct {
		skill string
		level float64
	}{
		{"TestMultiA", 0},
		{"TestMultiB", 0},
	}), 0.98, 0.02)

	// Two skills, both hard (level 20 vs skill 10 = 10% each)
	// With unified formula, both contribute negative expected scores, ~5% combined
	assertClose(t, testMulti([]struct {
		skill string
		level float64
	}{
		{"TestMultiA", 20},
		{"TestMultiB", 20},
	}), 0.05, 0.02)
}

func TestCheckWithDetails(t *testing.T) {
	cfg := NewServerConfig()

	// Create a test object with some skills
	obj := &Object{
		Unsafe: &ObjectDO{
			Id: "tester",
			Skills: map[string]Skill{
				"climbing": {Name: "climbing", Practical: 50, Theoretical: 50},
				"jumping":  {Name: "jumping", Practical: 10, Theoretical: 10},
			},
		},
	}

	t.Run("empty challenges returns success", func(t *testing.T) {
		challenges := Challenges{}
		score, failure := challenges.CheckWithDetails(cfg, obj, "target")
		if score != 1.0 {
			t.Errorf("expected score 1.0, got %v", score)
		}
		if failure != nil {
			t.Errorf("expected nil failure, got %v", failure)
		}
	})

	t.Run("easy challenge returns success with nil failure", func(t *testing.T) {
		// Reset skills
		obj.Unsafe.Skills["climbing"] = Skill{Name: "climbing", Practical: 50, Theoretical: 50}

		// Very easy challenge (level 0 vs skill 50) - should almost always pass
		challenges := Challenges{{Skill: "climbing", Level: 0, Message: "Too slippery"}}
		score, failure := challenges.CheckWithDetails(cfg, obj, "target")
		if score <= 0 {
			t.Errorf("expected positive score for easy challenge, got %v", score)
		}
		if failure != nil {
			t.Errorf("expected nil failure on success, got %v", failure)
		}
	})

	t.Run("hard challenge returns failure with primary failure", func(t *testing.T) {
		// Reset skills
		obj.Unsafe.Skills["climbing"] = Skill{Name: "climbing", Practical: 10, Theoretical: 10}

		// Very hard challenge (level 100 vs skill 10) - should almost always fail
		challenges := Challenges{{Skill: "climbing", Level: 100, Message: "Too slippery"}}
		score, failure := challenges.CheckWithDetails(cfg, obj, "target")
		if score > 0 {
			t.Errorf("expected negative score for hard challenge, got %v", score)
		}
		if failure == nil {
			t.Errorf("expected non-nil failure on failure")
		} else if failure.Message != "Too slippery" {
			t.Errorf("expected failure message 'Too slippery', got %q", failure.Message)
		}
	})

	t.Run("primary failure is the worst scoring challenge", func(t *testing.T) {
		// Reset skills with clean state
		obj.Unsafe.Skills["climbing"] = Skill{Name: "climbing", Practical: 10, Theoretical: 10, LastBase: 1}
		obj.Unsafe.Skills["jumping"] = Skill{Name: "jumping", Practical: 10, Theoretical: 10, LastBase: 1}

		// Two challenges: easy climbing, hard jumping
		// With unified formula, harder challenges produce worse failure scores,
		// so jumping (skill 10 vs level 50) will always score worse than
		// climbing (skill 10 vs level 10).
		challenges := Challenges{
			{Skill: "climbing", Level: 10, Message: "Slippery"},
			{Skill: "jumping", Level: 50, Message: "Too high"},
		}
		_, failure := challenges.CheckWithDetails(cfg, obj, "target")
		// The jumping challenge should be the primary failure (worst score)
		if failure == nil {
			t.Errorf("expected non-nil failure")
		} else if failure.Skill != "jumping" {
			t.Errorf("expected primary failure to be 'jumping', got %q", failure.Skill)
		}
	})
}

func TestLevel(t *testing.T) {
	cfg := NewServerConfig()

	u := skillUse{
		user: "a",
		skill: &Skill{
			Name:      "TestLevel",
			Practical: 10,
		},
		target: "b",
	}
	testAt := func(delta float64) float64 {
		success := 0
		count := 10000
		for range count {
			u.at = time.Unix(0, rand.Int64())
			u.challenge = float64(u.skill.Practical) + delta
			u.skill.Practical = 10
			u.skill.Theoretical = 10
			u.skill.LastBase = 1
			u.skill.LastUsedAt = 0
			if u.check(cfg, false) > 0 {
				success++
			}
		}
		return float64(success) / float64(count)
	}
	assertClose(t, testAt(20), 0.01, 0.01)
	assertClose(t, testAt(10), 0.1, 0.02)
	assertClose(t, testAt(0), 0.5, 0.02)
	assertClose(t, testAt(-10), 0.9, 0.02)
	assertClose(t, testAt(-20), 0.99, 0.02)
}

func TestRechargeWithoutReuse(t *testing.T) {
	cfg := NewServerConfig()

	u := skillUse{
		user: "a",
		skill: &Skill{
			Name:      "TestRechargeWithoutReuse",
			Practical: 10,
		},
		target: "b",
	}
	recharge := time.Minute
	cfg.SetSkillConfig("TestRechargeWithoutReuse", SkillConfig{
		Recharge: Duration(recharge),
	})
	testAt := func(multiple float64) float64 {
		success := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.skill.LastUsedAt = Stamp(time.Unix(0, rand.Int64())).Uint64()
			u.at = Timestamp(u.skill.LastUsedAt).Time().Add(time.Duration(float64(recharge) * multiple))
			u.challenge = u.skill.Effective(cfg, Stamp(u.at))
			if u.check(cfg, false) > 0 {
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
	cfg := NewServerConfig()

	now := time.Time{}
	s := &Skill{
		Name:        "TestForget",
		Practical:   20,
		Theoretical: 20,
		LastUsedAt:  Stamp(now).Uint64(),
	}
	forget := time.Hour
	cfg.SetSkillConfig("TestForget", SkillConfig{
		Forget: Duration(forget),
	})
	assertClose(t, s.Effective(cfg, Stamp(now)), 20, 0.02)
	assertClose(t, s.Effective(cfg, Stamp(now.Add(forget))), 15, 0.02)
	assertClose(t, s.Effective(cfg, Stamp(now.Add(forget*2))), 10, 0.02)

	skillUse{
		user:      "a",
		skill:     s,
		target:    "b",
		at:        now.Add(forget),
		challenge: 10,
	}.check(cfg, true)
	assertClose(t, s.Effective(cfg, Stamp(now.Add(forget))), 15, 0.04)
	assertClose(t, s.Practical, 15, 0.04)
}

func TestLearn(t *testing.T) {
	cfg := NewServerConfig()

	now := time.Time{}
	s := &Skill{
		Name:       "TestLearn",
		LastUsedAt: Stamp(now).Uint64(),
	}
	recharge := 6 * time.Minute
	cfg.SetSkillConfig("TestLearn", SkillConfig{
		Recharge: Duration(recharge),
		Forget:   Duration(time.Hour * 24 * 31 * 6),
	})
	timeTo := func(target float32, multiple float64) time.Duration {
		step := time.Duration(multiple * float64(recharge))
		dur := time.Duration(0)
		daily := time.Duration(0)
		var at time.Time
		for s.Practical < target {
			if daily < time.Hour*2 {
				at = Timestamp(s.LastUsedAt).Time().Add(step)
				dur += step
			} else {
				daily = 0
				at = Timestamp(s.LastUsedAt).Time().Add(time.Hour * 22)
			}
			dur += step
			//			before := s.Effective(cfg, Stamp(at))
			skillUse{
				user:      "a",
				skill:     s,
				target:    "b",
				at:        at,
				challenge: s.Effective(cfg, Stamp(at)),
			}.check(cfg, true)
		}
		return dur
	}
	assertClose(t, timeTo(5, 1.0), 37*time.Hour, time.Hour)
	s.Practical = 0
	s.Theoretical = 0
	assertClose(t, timeTo(10, 1.0), 100*time.Hour, time.Hour)
	s.Practical = 0
	s.Theoretical = 0
	assertClose(t, timeTo(10, 0.5), 99*time.Hour, time.Hour)
	s.Practical = 0
	s.Theoretical = 0
	assertClose(t, timeTo(10, 2.0), 199*time.Hour, time.Hour)
	s.Practical = 5
	s.Theoretical = 5
	assertClose(t, timeTo(10, 1.0), 62*time.Hour, time.Hour)
	s.Practical = 5
	s.Theoretical = 10
	assertClose(t, timeTo(10, 1.0), 12*time.Hour, time.Hour)
	s.Practical = 9
	s.Theoretical = 10
	assertClose(t, timeTo(10, 1.0), 7*time.Hour, time.Hour)
	s.Practical = 9
	s.Theoretical = 9
	assertClose(t, timeTo(10, 1.0), 15*time.Hour, time.Hour)
	s.Practical = 0
	s.Theoretical = 0
	assertClose(t, timeTo(20, 1.0), 386*time.Hour, time.Hour)
}

func TestRechargeWithReuse(t *testing.T) {
	cfg := NewServerConfig()

	u := skillUse{
		user: "a",
		skill: &Skill{
			Name:      "TestRechargeWithReuse",
			Practical: 10,
		},
		target: "b",
	}
	recharge := time.Minute
	cfg.SetSkillConfig("TestRechargeWithReuse", SkillConfig{
		Recharge: Duration(recharge),
		Reuse:    0.5,
	})
	testAt := func(multiple float64) float64 {
		count := 10000
		success := 0
		for i := 0; i < count; i++ {
			u.skill.LastBase = 0
			u.skill.LastUsedAt = 0
			u.at = time.Unix(0, rand.Int64())
			u.challenge = 0
			u.check(cfg, false)
			u.at = u.at.Add(time.Duration(float64(recharge) * multiple))
			u.challenge = u.skill.Effective(cfg, Stamp(u.at))
			if u.check(cfg, false) > 0 {
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
	cfg := NewServerConfig()

	u := skillUse{
		user: "a",
		skill: &Skill{
			Name:      "TestDuration",
			Practical: 10,
		},
		target: "b",
	}
	cfg.SetSkillConfig("TestDuration", SkillConfig{
		Duration: Duration(time.Minute),
	})
	testAt := func(multiple float64) float64 {
		same := 0
		count := 10000
		for i := 0; i < count; i++ {
			u.at = time.Unix(0, rand.Int64())
			val1 := u.rng(cfg).Float64()
			u.at = u.at.Add(time.Duration(float64(time.Minute) * multiple))
			val2 := u.rng(cfg).Float64()
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
