package skills

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/heap"
)

var (
	Skills = juicemud.NewSyncMap[string, Skill]()
)

type SkillDuration float32

func (s SkillDuration) Nanoseconds() int64 {
	return s.Duration().Nanoseconds()
}

func (s SkillDuration) Duration() time.Duration {
	return time.Duration(float64(time.Second) * float64(s))
}

type Skill struct {
	// Approximate time a skill check will be reused.
	Duration SkillDuration
	// Time for skill to recharge.
	Recharge SkillDuration
	// Success likelihood multiplier when immediately reused.
	Reuse float32
}

type Use struct {
	User  string
	Skill string
	At    time.Time

	reuse float32
}

func (s Use) RNG(target string) *rand.Rand {
	skill, foundSkill := Skills.GetHas(s.Skill)
	if skill.Duration == 0 {
		foundSkill = false
	}

	// Seed a hash with who does what to whom.
	h := fnv.New64()
	h.Write([]byte(s.User))
	h.Write([]byte(target))
	h.Write([]byte(s.Skill))

	// Seed the hash with time step based on skill duration.
	var step uint64
	if foundSkill {
		step = uint64(s.At.UnixNano() / skill.Duration.Nanoseconds() / 3)
	} else {
		step = uint64(s.At.UnixNano())
	}
	b := make([]byte, binary.Size(step))
	binary.BigEndian.PutUint64(b, step)
	h.Write(b)

	// Use the hash to seed an rng.
	result := rand.New(rand.NewSource(int64(h.Sum64())))

	if foundSkill {
		offset := result.Int63n(skill.Duration.Nanoseconds())
		if uint64((s.At.UnixNano()+offset)/skill.Duration.Nanoseconds()/3) != step {
			result.Float64()
		}
	}

	return result
}

func (s Use) RechargedAt() time.Time {
	return s.At.Add(Skills.Get(s.Skill).Recharge.Duration())
}

func (s Use) key() string {
	return fmt.Sprintf("%s.%s", s.User, s.Skill)
}

type Application struct {
	Use       Use
	Target    string
	Level     float32
	Challenge float32
}

func (s Application) Check() bool {
	// Success likelihood is ELO with 10 instead of 400 as "90% likely to win delta".
	success := skillUses.reuse(s.Use) / float32(1.0+math.Pow(10, float64(s.Level-s.Challenge)*0.1))
	return s.Use.RNG(s.Target).Float32() > success
}

type globalSkillUses struct {
	oldestUses    *heap.Heap[Use]
	mostRecentUse map[string]Use
	mutex         sync.Mutex
}

var (
	skillUses = &globalSkillUses{
		oldestUses: heap.New(func(a, b Use) bool {
			return a.RechargedAt().Before(b.RechargedAt())
		}),
		mostRecentUse: map[string]Use{},
	}
)

func (g *globalSkillUses) reuse(s Use) (result float32) {
	sk, found := Skills.GetHas(s.Skill)
	if !found {
		return 1.0
	}

	g.mutex.Lock()
	defer g.mutex.Unlock()

	defer func() {
		if mostRecent, found := g.mostRecentUse[s.key()]; !found || !mostRecent.At.After(s.At) {
			s.reuse = result
			g.mostRecentUse[s.key()] = s
		}
		g.oldestUses.Push(s)
	}()

	for oldest, found := g.oldestUses.Peek(); found && oldest.RechargedAt().Before(s.At); oldest, found = g.oldestUses.Peek() {
		key := oldest.key()
		if latest, found := g.mostRecentUse[key]; found && latest.At.UnixNano() <= oldest.At.UnixNano() {
			delete(g.mostRecentUse, key)
		}
		g.oldestUses.Pop()
	}

	if old, found := g.mostRecentUse[s.key()]; found {
		sinceLastUse := float64(s.At.Sub(old.At))
		rechargeFraction := sinceLastUse / float64(sk.Recharge.Nanoseconds())
		rechargeFactor := float32(math.Pow(0.5, -(8*rechargeFraction-8)) - math.Pow(0.5, 8))
		practicalReuse := old.reuse * sk.Reuse
		return float32(math.Min(1, float64((practicalReuse + (1-practicalReuse)*rechargeFactor))))
	} else {
		return 1
	}
}
