//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
//go:generate go run ../decorator/decorator.go -in schema.go -out decorated.go -pkg structs
package structs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"hash/fnv"
	"iter"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/lang"

	rnd "math/rand"

	goccy "github.com/goccy/go-json"
)

var (
	lastEventCounter  uint64 = 0
	lastObjectCounter uint64 = 0
	encoding                 = base64.StdEncoding.WithPadding(base64.NoPadding)
)

const (
	objectIDLen = 16
)

type Timestamp uint64

func (t Timestamp) Uint64() uint64 {
	return uint64(t)
}

func (t Timestamp) Nanoseconds() int64 {
	return int64(t)
}

func (t Timestamp) Time() time.Time {
	return time.Unix(0, int64(t))
}

func Stamp(t time.Time) Timestamp {
	return Timestamp(t.UnixNano())
}

func NextObjectID() (string, error) {
	objectCounter := juicemud.Increment(&lastObjectCounter)
	timeSize := binary.Size(objectCounter)
	result := make([]byte, objectIDLen)
	binary.BigEndian.PutUint64(result, objectCounter)
	if _, err := rand.Read(result[timeSize:]); err != nil {
		return "", juicemud.WithStack(err)
	}
	return encoding.EncodeToString(result), nil
}

type Serializable[T any] interface {
	Marshal([]byte)
	Unmarshal([]byte) error
	Size() int
	*T
}

type Snapshottable[T any] interface {
	Serializable[T]
	Lock()
	Unlock()
	RLock()
	RUnlock()
	SetPostUnlock(func(t *T))
	GetId() string
	Describe() string
	UnsafeShallowCopy() *T
}

func Clone[T any, S Serializable[T]](t *T) (*T, error) {
	s := S(t)
	b := make([]byte, s.Size())
	s.Marshal(b)
	result := new(T)
	if err := S(result).Unmarshal(b); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (e *Exit) Name() string {
	if len(e.Descriptions) == 0 {
		return "nameless"
	}
	return e.Descriptions[0].Short
}

func (o *Object) Name() string {
	if descs := o.GetDescriptions(); len(descs) > 0 {
		return descs[0].Short
	}
	return "nameless"
}

func (o *Object) Unique() bool {
	if descs := o.GetDescriptions(); len(descs) > 0 {
		return descs[0].Unique
	}
	return false
}

func (o *Object) Indef() string {
	name := o.Name()
	if o.Unique() {
		return name
	}
	return lang.Indef(name)
}

func (o *Object) HasCallback(name string, tag string) bool {
	callbacks := o.GetCallbacks()
	tags, found := callbacks[name]
	if !found {
		return false
	}
	_, found = tags[tag]
	return found
}

func DressObject(obj *Object) *Object {
	obj.Lock()
	defer obj.Unlock()
	if obj.Unsafe.Callbacks == nil {
		obj.Unsafe.Callbacks = map[string]map[string]bool{}
	}
	if obj.Unsafe.Content == nil {
		obj.Unsafe.Content = map[string]bool{}
	}
	if obj.Unsafe.Skills == nil {
		obj.Unsafe.Skills = map[string]Skill{}
	}
	return obj
}

func MakeObject(ctx context.Context) (*Object, error) {
	newID, err := NextObjectID()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return DressObject(&Object{
		Unsafe: &ObjectDO{
			Id: newID,
		},
	}), nil
}

func (e *Event) CreateKey() {
	eventCounter := juicemud.Increment(&lastEventCounter)
	atSize := binary.Size(e.At)
	k := make([]byte, atSize+binary.Size(eventCounter))
	binary.BigEndian.PutUint64(k, uint64(e.At))
	binary.BigEndian.PutUint64(k[atSize:], eventCounter)
	e.Key = string(k)
}

func (c *Challenge) Check(challenger *Object, targetID string) bool {
	challenger.Lock()
	defer challenger.Unlock()

	skill := challenger.Unsafe.Skills[c.Skill]
	skill.Name = c.Skill

	result := skillUse{
		skill:     &skill,
		user:      challenger.Unsafe.Id,
		challenge: float64(c.Level),
		at:        time.Now(),
		target:    targetID,
	}.check(challenger.GetLearning())

	challenger.Unsafe.Skills[c.Skill] = skill

	return result
}

type Challenges []Challenge

func (c Challenges) Merge(mergeChallenges map[string]Challenge) Challenges {
	newChallenges := Challenges{}
	for _, challenge := range c {
		if mergeChallenge, found := mergeChallenges[challenge.Skill]; found {
			challenge.Level += mergeChallenge.Level
			newChallenges = append(newChallenges, challenge)
		} else {
			newChallenges = append(newChallenges, mergeChallenge)
		}
	}
	return newChallenges
}

func (c Challenges) Map() map[string]Challenge {
	result := map[string]Challenge{}
	for _, challenge := range c {
		result[challenge.Skill] = challenge
	}
	return result
}

func (c Challenges) Check(challenger *Object, targetID string) bool {
	for _, challenge := range c {
		if !challenge.Check(challenger, targetID) {
			return false
		}
	}
	return true
}

type Descriptions []Description

func (d Descriptions) Matches(pattern string) bool {
	for _, desc := range d {
		if match, _ := filepath.Match(pattern, desc.Short); match {
			return true
		}
	}
	return false
}

// TODO: Rename to "FirstDetected"
func (d Descriptions) Detect(viewer *Object, targetID string) *Description {
	for _, desc := range d {
		if Challenges(desc.Challenges).Check(viewer, targetID) {
			return &desc
		}
	}
	return nil
}

func (o *Object) AddDescriptionChallenges(addedChallenges Challenges) (*Object, error) {
	cpy, err := Clone(o)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	mergeChallenges := addedChallenges.Map()
	for currDescIdx := range cpy.Unsafe.Descriptions {
		cpy.Unsafe.Descriptions[currDescIdx].Challenges = Challenges(cpy.Unsafe.Descriptions[currDescIdx].Challenges).Merge(mergeChallenges)
	}

	return cpy, nil
}

func (o *Object) Filter(viewer *Object) (*Object, error) {
	cpy, err := Clone(o)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	if desc := Descriptions(cpy.Unsafe.Descriptions).Detect(viewer, cpy.Unsafe.Id); desc != nil {
		cpy.Unsafe.Descriptions = []Description{*desc}
	} else {
		cpy.Unsafe.Descriptions = nil
	}

	exits := Exits{}
	for _, exit := range cpy.Unsafe.Exits {
		if exitDesc := Descriptions(exit.Descriptions).Detect(viewer, cpy.Unsafe.Id); exitDesc != nil {
			exit.Descriptions = []Description{*exitDesc}
			exits = append(exits, exit)
		}
	}
	cpy.Unsafe.Exits = exits

	return cpy, nil
}

type Exits []Exit

func (e Exits) Short() string {
	result := sort.StringSlice{}
	for _, exit := range e {
		if len(exit.Descriptions) == 0 {
			continue
		}
		result = append(result, exit.Descriptions[0].Short)
	}
	sort.Sort(result)
	return strings.Join(result, ", ")
}

type Content map[string]*Object

func (c Content) Short() []string {
	result := sort.StringSlice{}

	indef := map[string]int{}
	for _, obj := range c {
		name := obj.Name()
		if obj.Unique() {
			result = append(result, name)
		} else {
			indef[name] += 1
		}
	}

	for name, count := range indef {
		result = append(result, lang.Card(count, name))
	}

	sort.Sort(result)
	return result
}

func (c Content) Sorted() iter.Seq2[string, *Object] {
	return func(yield func(string, *Object) bool) {
		keys := make(sort.StringSlice, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		sort.Sort(keys)
		for _, k := range keys {
			if !yield(k, c[k]) {
				return
			}
		}
	}
}

type Location struct {
	Container *Object
	Content   Content
}

func (l *Location) Describe() string {
	b, _ := goccy.MarshalIndent(l, "", "  ")
	return string(b)
}

func (l *Location) AddDescriptionChallenges(addedChallenges Challenges) (*Location, error) {
	result := &Location{
		Content: Content{},
	}
	var err error
	result.Container, err = l.Container.AddDescriptionChallenges(addedChallenges)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	for id := range l.Content {
		if result.Content[id], err = l.Content[id].AddDescriptionChallenges(addedChallenges); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}

	return result, nil
}

func (l *Location) Filter(viewer *Object) (*Location, error) {
	result := &Location{
		Content: Content{},
	}
	for id := range l.Content {
		if id == viewer.GetId() {
			result.Content[id] = l.Content[id]
		} else if cont, err := l.Content[id].Filter(viewer); err != nil {
			return nil, juicemud.WithStack(err)
		} else if len(cont.GetDescriptions()) > 0 {
			result.Content[id] = cont
		}
	}
	var err error
	if result.Container, err = l.Container.Filter(viewer); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (l *Location) All() iter.Seq[*Object] {
	return func(yield func(*Object) bool) {
		if !yield(l.Container) {
			return
		}
		for _, v := range l.Content {
			if !yield(v) {
				return
			}
		}
	}
}

var indexRegexp = regexp.MustCompile(`^((\d+)\.)?(.*)$`)

func (l *Location) Identify(s string) (*Object, error) {
	match := indexRegexp.FindStringSubmatch(s)
	indexString, pattern := match[2], match[3]
	index := -1
	if indexString != "" {
		var err error
		if index, err = strconv.Atoi(indexString); err != nil {
			return nil, errors.Errorf("%q isn't a number", indexString)
		}
	}
	objs := []*Object{}
	if Descriptions(l.Container.GetDescriptions()).Matches(pattern) {
		objs = append(objs, l.Container)
	}
	for _, cont := range l.Content.Sorted() {
		if Descriptions(cont.GetDescriptions()).Matches(pattern) {
			objs = append(objs, cont)
		}
	}
	if len(objs) == 0 {
		return nil, errors.Errorf("No %q found", pattern)
	} else if len(objs) == 1 && (index == 0 || index == -1) {
		return objs[0], nil
	} else if index == -1 {
		return nil, errors.Errorf("%v %q found, pick one", len(objs), pattern)
	} else if index < len(objs) {
		return objs[index], nil
	}
	return nil, errors.Errorf("Only %v %q found", len(objs), pattern)
}

type Detection struct {
	Subject *Object
	Object  *Object
}

func (l *Location) Detections(target *Object, addedChallenges Challenges) iter.Seq2[*Detection, error] {
	return func(yield func(*Detection, error) bool) {
		for viewer := range l.All() {
			if viewer.GetId() != target.GetId() {
				if challenged, err := target.AddDescriptionChallenges(addedChallenges); err != nil {
					if !yield(nil, juicemud.WithStack(err)) {
						return
					}
				} else if filtered, err := challenged.Filter(viewer); err != nil {
					if !yield(nil, juicemud.WithStack(err)) {
						return
					}
				} else if len(filtered.GetDescriptions()) > 0 {
					if !yield(&Detection{Subject: viewer, Object: filtered}, nil) {
						return
					}
				}
			}
		}
	}
}

func (n *DeepNeighbourhood) Detections(target *Object) iter.Seq2[*Detection, error] {
	return func(yield func(*Detection, error) bool) {
		for det, err := range n.Location.Detections(target, nil) {
			if !yield(det, err) {
				return
			}
		}
		for _, neighbour := range n.Neighbours {
			for _, exit := range neighbour.Container.GetExits() {
				if exit.Destination == n.Location.Container.GetId() {
					for det, err := range neighbour.Detections(target, Challenges(exit.TransmitChallenges)) {
						if !yield(det, err) {
							return
						}
					}
				}
			}
		}
	}
}

type Neighbourhood struct {
	Location   *Object
	Neighbours map[string]*Object
}

func (n *Neighbourhood) Describe() string {
	b, _ := goccy.MarshalIndent(n, "", "  ")
	return string(b)
}

func (n *Neighbourhood) FindLocation(locID string) (*Exit, bool) {
	if n.Location.GetId() == locID {
		return nil, true
	}
	for _, exit := range n.Location.GetExits() {
		if neigh, found := n.Neighbours[exit.Destination]; found && neigh.GetId() == locID {
			return &exit, true
		}
	}
	return nil, false
}

type DeepNeighbourhood struct {
	Location   *Location
	Neighbours map[string]*Location
}

func (n *DeepNeighbourhood) Describe() string {
	b, _ := goccy.MarshalIndent(n, "", "  ")
	return string(b)
}

func (n *DeepNeighbourhood) Filter(viewer *Object) (*DeepNeighbourhood, error) {
	result := &DeepNeighbourhood{
		Neighbours: map[string]*Location{},
	}

	var err error
	if result.Location, err = n.Location.Filter(viewer); err != nil {
		return nil, err
	}

	for _, exit := range n.Location.Container.GetExits() {
		if neighbour, found := n.Neighbours[exit.Destination]; found {
			if challenged, err := neighbour.AddDescriptionChallenges(exit.TransmitChallenges); err != nil {
				return nil, juicemud.WithStack(err)
			} else if filtered, err := challenged.Filter(viewer); err != nil {
				return nil, juicemud.WithStack(err)
			} else if len(filtered.Container.GetDescriptions()) > 0 {
				result.Neighbours[exit.Destination] = filtered
			}
		}
	}

	return result, nil
}

func (n *DeepNeighbourhood) All() iter.Seq[*Object] {
	return func(yield func(*Object) bool) {
		for v := range n.Location.All() {
			if !yield(v) {
				return
			}
		}
		for _, loc := range n.Neighbours {
			for v := range loc.All() {
				if !yield(v) {
					return
				}
			}
		}
	}
}

func (c *Call) Call() (*Call, error) {
	return c, nil
}

type Caller interface {
	Call() (*Call, error)
}

type AnyCall struct {
	Name    string
	Tag     string
	Content any
}

func (a *AnyCall) Call() (*Call, error) {
	js, err := goccy.Marshal(a.Content)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &Call{
		Name:    a.Name,
		Tag:     a.Tag,
		Message: string(js),
	}, nil
}

func (e *Event) Event() (*Event, error) {
	return e, nil
}

type Eventer interface {
	Event() (*Event, error)
}

type AnyEvent struct {
	At     Timestamp
	Object string
	Caller Caller
	Key    string
}

func (a *AnyEvent) Event() (*Event, error) {
	call, err := a.Caller.Call()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &Event{
		At:     uint64(a.At),
		Object: a.Object,
		Call:   *call,
		Key:    a.Key,
	}, nil
}

var (
	SkillConfigs = juicemud.NewSyncMap[string, SkillConfig]()
)

type SkillDuration float64

func (s SkillDuration) Nanoseconds() int64 {
	return s.Duration().Nanoseconds()
}

func (s SkillDuration) Duration() time.Duration {
	return time.Duration(float64(time.Second) * float64(s))
}

func Duration(d time.Duration) SkillDuration {
	return SkillDuration(float64(d) / float64(time.Second))
}

type SkillConfig struct {
	// Time after a skill check is 50% likely to be reused.
	Duration SkillDuration
	// Time for a skill to be fully ready for reuse.
	Recharge SkillDuration
	// Multiplier for success chance when imediately reused.
	Reuse float64
	// Time for skill to be forgotten down to 50% of theoretical level.
	Forget SkillDuration
}

func (s *Skill) specificRecharge(at Timestamp, recharge SkillDuration) float64 {
	nanosSinceLastUse := at.Nanoseconds() - Timestamp(s.LastUsedAt).Nanoseconds()
	rechargeFraction := float64(nanosSinceLastUse) / float64(recharge.Nanoseconds())
	return math.Min(1, math.Pow(0.5, -(8*rechargeFraction-8))-math.Pow(0.5, 8))
}

func (s *Skill) improvement(at Timestamp, challenge float64, effective float64) float64 {
	recharge := 6 * time.Minute
	if sk, found := SkillConfigs.GetHas(s.Name); found && sk.Recharge.Duration() > recharge {
		recharge = sk.Recharge.Duration()
	}
	rechargeCoeff := math.Min(1, float64(at.Time().Sub(Timestamp(s.LastUsedAt).Time()))/float64(recharge))
	skillCoeff := 0.0355 * math.Pow(0.9, effective)
	theoryCoeff := math.Max(1, float64(1+3*(s.Theoretical-s.Practical)))
	challengeCoeff := 1 / (1 + math.Abs(challenge-effective))
	perUse := float64(recharge) / float64(6*time.Minute)
	return rechargeCoeff * skillCoeff * theoryCoeff * challengeCoeff * perUse
}

// Returns the effective level of this skill considering amount forgotten since last use.
func (s *Skill) Effective(at Timestamp) float64 {
	if config, found := SkillConfigs.GetHas(s.Name); found && config.Forget != 0 {
		nanosSinceLastUse := at.Nanoseconds() - Timestamp(s.LastUsedAt).Nanoseconds()
		forgetFraction := float64(nanosSinceLastUse) / float64(config.Forget.Nanoseconds())
		forgetCoeff := 1 + (-1 / (1 + math.Exp(8-8*forgetFraction))) + (1 / math.Exp(8))
		permanentSkill := 0.5 * s.Theoretical
		forgettableSkill := float64(s.Practical - permanentSkill)
		return forgettableSkill*forgetCoeff + float64(permanentSkill)
	}

	return float64(s.Practical)
}

// Returns the amount of skill level useable considering recharge time.
func (s *Skill) rechargeCoeff(at Timestamp) float64 {
	if s.LastUsedAt == 0 {
		return 1.0
	}

	if sk, found := SkillConfigs.GetHas(s.Name); found && sk.Recharge != 0 {
		rechargeCoeff := s.specificRecharge(at, sk.Recharge)
		cumulativeReuse := float64(s.LastBase) * sk.Reuse
		return cumulativeReuse + (1-cumulativeReuse)*rechargeCoeff
	}

	return 1.0
}

type skillUse struct {
	skill     *Skill
	user      string
	target    string
	challenge float64
	at        time.Time
}

func (s skillUse) check(improve bool) bool {
	stamp := Stamp(s.at)

	effective := float64(s.skill.Practical)
	if improve {
		effective = s.skill.Effective(stamp)
		s.skill.Practical = float32(effective)
	}

	rechargeCoeff := s.skill.rechargeCoeff(stamp)
	successChance := rechargeCoeff / (1.0 + math.Pow(10, (s.challenge-effective)*0.1))

	if improve {
		s.skill.Practical += float32(s.skill.improvement(stamp, s.challenge, effective))
		if s.skill.Practical > s.skill.Theoretical {
			s.skill.Theoretical = s.skill.Practical
		}
	}

	s.skill.LastBase = float32(rechargeCoeff)
	s.skill.LastUsedAt = stamp.Uint64()

	return s.rng().Float64() < successChance
}

func (s skillUse) rng() *rnd.Rand {
	// Seed a hash with who does what to whom.
	h := fnv.New64()
	h.Write([]byte(s.user))
	h.Write([]byte(s.skill.Name))
	h.Write([]byte(s.target))

	skillConfig := SkillConfigs.Get(s.skill.Name)

	// Seed the hash with time step based on skill duration.
	step := uint64(s.at.UnixNano())
	if skillConfig.Duration != 0 {
		step = uint64(s.at.UnixNano() / skillConfig.Duration.Nanoseconds() / 3)
	}
	b := make([]byte, binary.Size(step))
	binary.BigEndian.PutUint64(b, step)
	h.Write(b)

	// Use the hash to seed an rng.
	result := rnd.New(rnd.NewSource(int64(h.Sum64())))

	// If the skill has a duration then reseed with a second step based on a random offset.
	if skillConfig.Duration != 0 {
		offset := result.Int63n(skillConfig.Duration.Nanoseconds())
		binary.BigEndian.PutUint64(b, uint64((s.at.UnixNano()+offset)/skillConfig.Duration.Nanoseconds()/3))
		h.Write(b)
		result = rnd.New(rnd.NewSource(int64(h.Sum64())))
	}

	return result
}

type Objects []*Object

func (o Objects) Len() int {
	return len(o)
}

func (o Objects) Less(i, j int) bool {
	return strings.Compare(o[i].GetId(), o[i].GetId()) < 0
}

func (o Objects) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}

func (o Objects) Lock() {
	sort.Sort(o)
	for _, obj := range o {
		obj.Lock()
	}
}

func (o Objects) Unlock() {
	for _, obj := range o {
		obj.Unlock()
	}
}

func WithLock(f func() error, objs ...*Object) error {
	toLock := make(Objects, 0, len(objs))
	seen := map[*Object]bool{}
	for _, obj := range objs {
		if obj == nil {
			return errors.New("can't lock nil object")
		}
		if !seen[obj] {
			toLock = append(toLock, obj)
			seen[obj] = true
		}
	}
	toLock.Lock()
	defer toLock.Unlock()

	return f()
}
