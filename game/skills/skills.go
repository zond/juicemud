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
	// Skills might have Duration.
	// This takes the shape of the result of the skill check not changing for a given
	// subject, object, and skill when used again.
	// The Duration of a skill is n seconds, where the likelihood that the skill check
	// is the same after n seconds is 50%, and the likelihood that the skill check is
	// the same after 3 x n seconds is 0%.
	Duration SkillDuration
	// Skills might take time to Recharge.
	// This takes the shape of a skill success likelihood being multiplied with
	// a factor that starts at 0 when the skill was last used by the same subject,
	// and gets 50% closer to 1 every n seconds.
	// The Recharge of a skill is 8 * n seconds, i.e. when the factor is
	// 1 - 0.5^8 ~= 0.996.
	// TL;DR Recharge is when the skill is freely usable again. 0 means immediately.
	Recharge SkillDuration
}

type Use struct {
	User  string
	Skill string
	At    time.Time
}

func (s Use) RNG(target string) *rand.Rand {
	skill, foundSkill := Skills.GetHas(s.Skill)

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

func (s Use) Recharge() time.Time {
	if sk, found := Skills.GetHas(s.Skill); found {
		return s.At.Add(sk.Recharge.Duration())
	}
	return s.At
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
	// success := float64(skillUses.recharge(s.use)) / (1.0 + math.Pow(10, float64(s.challenge-s.level)*0.1))
	success := skillUses.recharge(s.Use) / float32(1.0+math.Pow(10, float64(s.Level-s.Challenge)*0.1))
	return s.Use.RNG(s.Target).Float32() > success
}

type globalSkillUses struct {
	heap  *heap.Heap[Use]
	uses  map[string]Use
	mutex sync.Mutex
}

var (
	skillUses = &globalSkillUses{
		heap: heap.New(func(a, b Use) bool {
			return a.Recharge().Before(b.Recharge())
		}),
		uses: map[string]Use{},
	}
)

func (g *globalSkillUses) recharge(s Use) float32 {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	defer func() {
		g.uses[s.key()] = s
		g.heap.Push(s)
	}()
	for oldest, found := g.heap.Peek(); found && oldest.Recharge().Before(s.At); oldest, found = g.heap.Peek() {
		key := oldest.key()
		if latest, found := g.uses[key]; found && latest.At.UnixNano() <= oldest.At.UnixNano() {
			delete(g.uses, key)
		}
		g.heap.Pop()
	}
	if old, found := g.uses[s.key()]; found {
		if sk, found := Skills.GetHas(s.Skill); found {
			return 1.0 - float32(math.Pow(0.5, 8*float64(s.At.Sub(old.At))/float64(sk.Recharge.Nanoseconds())))
		} else {
			return 1.0
		}
	} else {
		return 1.0
	}
}
