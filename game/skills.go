package game

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"math/rand"
	"time"
)

type skillUse struct {
	subject   string
	object    string
	challenge float32
	at        time.Time
}

type skillAndUse struct {
	skill *skill
	use   *skillUse
}

type skillUses struct {
	uses map[time.Time][]*skillAndUse
}

func (s *skillUses) use(use *skillAndUse) float32 {

}

type skill struct {
	name     string
	level    float32
	duration time.Duration
}

func (s *skill) rng(use *skillUse) *rand.Rand {
	// Create rng based on subject, object, name, and time step.
	h := fnv.New64()
	h.Write([]byte(use.subject))
	h.Write([]byte(use.object))
	h.Write([]byte(s.name))
	now1 := use.at.UnixNano() / s.duration.Nanoseconds()
	nowBytes1 := make([]byte, binary.Size(now1))
	binary.BigEndian.PutUint64(nowBytes1, uint64(now1))
	h.Write(nowBytes1)
	rng1 := rand.New(rand.NewSource(int64(h.Sum64())))

	// Offset the time step with the rng, and create another one.
	offset := rng1.Int63n(s.duration.Nanoseconds())
	now2 := (time.Now().UnixNano() + offset) / s.duration.Microseconds()
	binary.BigEndian.PutUint64(nowBytes1, uint64(now2))
	h.Write(nowBytes1)

	return rand.New(rand.NewSource(int64(h.Sum64())))
}

func (s *skill) check(use *skillUse) bool {
	// Win rate is ELO with 10 instead of 400 as "90% likely to win delta".
	winRate := 1.0 / (1.0 + math.Pow(10, float64(use.challenge-s.level)*0.1))
	return s.rng(use).Float64() > winRate
}
