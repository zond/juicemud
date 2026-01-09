//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
//go:generate go run ../decorator/decorator.go -in schema.go -out decorated.go -pkg structs
package structs

import (
	"context"
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
	lastEventCounter uint64 = 0
)

// MinValue is the floor value for skill rolls to prevent -Inf and division by zero.
const MinValue = 1e-9

// SkillsKey returns a canonical string key for a set of skills.
// Skills are sorted alphabetically and comma-joined (e.g., "awareness,senses").
// Used for looking up ChallengeDurations and seeding deterministic RNG.
func SkillsKey(skills map[string]bool) string {
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// Context provides game-time and configuration access for skill operations.
// Game time pauses when the server is down, so skills don't decay/recharge
// during maintenance. The implementation lives in the game package.
type Context interface {
	context.Context
	Now() time.Time           // Game time (may differ from wall clock)
	ServerConfig() *ServerConfig
}

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

// Indef returns the object's name with an indefinite article ("a"/"an"),
// unless the object is unique (proper noun), in which case just the name.
func (o *Object) Indef() string {
	name := o.Name()
	if o.Unique() {
		return name
	}
	return lang.Indef(name)
}

// CanCombat returns true if the object can participate in combat.
// Combat requires BodyConfigID to be set and MaxHealth > 0.
func (o *Object) CanCombat() bool {
	o.RLock()
	defer o.RUnlock()
	return o.Unsafe.BodyConfigID != "" && o.Unsafe.MaxHealth > 0
}

// IsAlive returns true if the object has positive health.
// Only meaningful for objects that CanCombat(). For non-combat objects
// (where CanCombat() returns false), this will typically return false
// since Health defaults to 0.
func (o *Object) IsAlive() bool {
	o.RLock()
	defer o.RUnlock()
	return o.Unsafe.Health > 0
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

// UpdateState atomically reads, modifies, and writes the object's state as a typed value.
// The state is deserialized into T, passed to the update function, and serialized back.
// The object is locked during the entire operation.
func UpdateState[T any](obj *Object, update func(*T) error) error {
	obj.Lock()
	defer obj.Unlock()

	var data T
	if obj.Unsafe.State != "" && obj.Unsafe.State != "{}" {
		if err := goccy.Unmarshal([]byte(obj.Unsafe.State), &data); err != nil {
			return err
		}
	}

	if err := update(&data); err != nil {
		return err
	}

	newState, err := goccy.Marshal(data)
	if err != nil {
		return err
	}
	obj.Unsafe.State = string(newState)
	return nil
}

// FindExit returns a pointer to a copy of the exit leading to destination, or nil if not found.
// The returned pointer is heap-allocated and safe to store beyond the Object's lifetime.
func (o *Object) FindExit(destination string) *Exit {
	exits := o.GetExits()
	for i := range exits {
		if exits[i].Destination == destination {
			exitCopy := exits[i]
			return &exitCopy
		}
	}
	return nil
}

// PostUnmarshal initializes nil map fields on an ObjectDO to empty maps.
// Called automatically by Object.Unmarshal via PostUnmarshaler interface.
func (o *ObjectDO) PostUnmarshal() {
	if o.Callbacks == nil {
		o.Callbacks = map[string]map[string]bool{}
	}
	if o.Content == nil {
		o.Content = map[string]bool{}
	}
	if o.Skills == nil {
		o.Skills = map[string]Skill{}
	}
	if o.BodyParts == nil {
		o.BodyParts = map[string]BodyPartState{}
	}
}

func MakeObject(ctx context.Context) (*Object, error) {
	obj := &Object{
		Unsafe: &ObjectDO{
			Id: juicemud.NextUniqueID(),
		},
	}
	obj.Unsafe.PostUnmarshal()
	return obj, nil
}

// CreateKey generates a unique, sortable key combining timestamp and counter.
func (e *Event) CreateKey() {
	eventCounter := juicemud.Increment(&lastEventCounter)
	atSize := binary.Size(e.At)
	k := make([]byte, atSize+binary.Size(eventCounter))
	binary.BigEndian.PutUint64(k, uint64(e.At))
	binary.BigEndian.PutUint64(k[atSize:], eventCounter)
	e.Key = string(k)
}

// Check tests the challenger's skills against this challenge's difficulty.
// Returns positive for success (score = 10 * log10(yourRoll / challengeRoll)), negative for failure.
// Uses uniform roll comparison - both sides roll in [0, 10^(level/10)].
// If rng is nil, generates one internally using multiSkillRng.
// If rng is provided, uses it (allows CheckWithDetails to continue the sequence for blame).
// Updates skill state (LastUsedAt, LastBase) and applies learning if enabled.
// IMPORTANT: Caller must hold write access to the challenger (modifies Skills).
func (c *Challenge) Check(ctx Context, challenger *Object, targetID string, rng *rnd.Rand) float64 {
	if !c.HasChallenge() {
		return 1 // No challenge means automatic success (positive score)
	}

	// Compute mean effective level (includes forgetting persistence and recharge folding)
	effective := challenger.EffectiveSkills(ctx, c.Skills)

	// Generate RNG if not provided
	if rng == nil {
		rng = multiSkillRng(ctx, c.Skills, challenger.Unsafe.Id, targetID)
	}

	// Your roll (also applies side effects: learning, state updates)
	yourRoll := challenger.Roll(ctx, c.Skills, targetID, effective, float64(c.Level), rng)

	// Challenge roll from same RNG sequence
	challengeMax := math.Pow(10, float64(c.Level)/10)
	challengeRoll := math.Max(MinValue, rng.Float64()*challengeMax)

	// Score: positive for success, negative for failure
	return 10 * math.Log10(yourRoll/challengeRoll)
}

// CheckWithDetails is like Check but also returns the name of a blamed skill on failure.
// Blame is probabilistic, weighted by inverse effective level (weaker skills blamed more often).
func (c *Challenge) CheckWithDetails(ctx Context, challenger *Object, targetID string) (float64, string) {
	// Generate RNG once, use for both check and blame
	rng := multiSkillRng(ctx, c.Skills, challenger.Unsafe.Id, targetID)

	score := c.Check(ctx, challenger, targetID, rng)
	if score > 0 {
		return score, ""
	}

	// Compute blame weights: weaker skills (lower effective) get higher weight
	// Weight = 1 / max(1, effective) so lower effective = higher probability of blame
	type skillWeight struct {
		name   string
		weight float64
	}
	weights := make([]skillWeight, 0, len(c.Skills))
	totalWeight := 0.0

	for name := range c.Skills {
		skill := challenger.Unsafe.Skills[name]
		effective := skill.chargedPracticalLevel(ctx)
		// Use inverse of effective as weight (clamped to avoid division by zero/negative)
		weight := 1.0 / math.Max(1, effective)
		weights = append(weights, skillWeight{name, weight})
		totalWeight += weight
	}

	// Use next value from same RNG sequence for consistent blame
	r := rng.Float64() * totalWeight

	// Pick skill based on weighted random selection
	cumulative := 0.0
	for _, sw := range weights {
		cumulative += sw.weight
		if r < cumulative {
			return score, sw.name
		}
	}

	// Fallback to last skill (shouldn't happen)
	if len(weights) > 0 {
		return score, weights[len(weights)-1].name
	}
	return score, ""
}

// HasChallenge returns true if the challenge has any skills defined.
func (c *Challenge) HasChallenge() bool {
	return len(c.Skills) > 0
}

// Merge combines two challenges by unioning their Skills and adding their Levels.
// If c has no skills, returns other. If other has no skills, returns c.
func (c Challenge) Merge(other Challenge) Challenge {
	if !c.HasChallenge() {
		return other
	}
	if !other.HasChallenge() {
		return c
	}
	result := Challenge{
		Skills:  make(map[string]bool, len(c.Skills)+len(other.Skills)),
		Level:   c.Level + other.Level, // Sum: stacked challenges are harder
		Message: c.Message,
	}
	for skill := range c.Skills {
		result.Skills[skill] = true
	}
	for skill := range other.Skills {
		result.Skills[skill] = true
	}
	if result.Message == "" {
		result.Message = other.Message
	}
	return result
}

type Descriptions []Description

func (d Descriptions) Matches(pattern string) bool {
	for _, desc := range d {
		// First try exact/glob match against full Short description
		if match, _ := filepath.Match(pattern, desc.Short); match {
			return true
		}
		// Then try glob match against individual words
		for _, word := range strings.Fields(desc.Short) {
			if match, _ := filepath.Match(pattern, word); match {
				return true
			}
		}
	}
	return false
}

// Detect returns all descriptions the viewer can perceive.
// This includes descriptions with no challenge (always visible) and
// descriptions where the viewer overcomes the challenge.
// Returns nil if no descriptions are visible.
func (d Descriptions) Detect(ctx Context, viewer *Object, targetID string) []Description {
	var result []Description
	for _, desc := range d {
		// No challenge = always visible, or check the challenge
		if !desc.Challenge.HasChallenge() || desc.Challenge.Check(ctx, viewer, targetID, nil) > 0 {
			result = append(result, desc)
		}
	}
	return result
}

// Long returns the concatenated Long texts from all descriptions.
// Empty Long texts are skipped, and non-empty texts are separated by a space.
func (d Descriptions) Long() string {
	var parts []string
	for _, desc := range d {
		if desc.Long != "" {
			parts = append(parts, desc.Long)
		}
	}
	return strings.Join(parts, " ")
}

// AddDescriptionChallenge returns a copy with added difficulty on all descriptions.
// The added challenge is merged into each description's existing challenge.
func (o *Object) AddDescriptionChallenge(added Challenge) (*Object, error) {
	if !added.HasChallenge() {
		return o, nil
	}
	cpy, err := Clone(o)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	for i := range cpy.Unsafe.Descriptions {
		cpy.Unsafe.Descriptions[i].Challenge = cpy.Unsafe.Descriptions[i].Challenge.Merge(added)
	}

	return cpy, nil
}

// EffectiveSkills computes the mean effective level for a set of skills.
// For each skill: applies forgetting (persisted to Practical), folds recharge into effective.
// Returns the arithmetic mean of all effective values.
// IMPORTANT: Must be called immediately before Roll since Roll updates LastUsedAt.
// IMPORTANT: Caller must hold write access to the object (modifies Skills).
func (o *Object) EffectiveSkills(ctx Context, skills map[string]bool) float64 {
	if len(skills) == 0 {
		return 0
	}

	sum := 0.0
	for name := range skills {
		skill := o.Unsafe.Skills[name]
		if skill.Name == "" {
			skill.Name = name
		}

		// Apply forgetting decay and persist to Practical
		skill.Practical = float32(skill.practicalPostForget(ctx))
		o.Unsafe.Skills[name] = skill

		// Get effective level (Practical + recharge penalty)
		sum += skill.chargedPracticalLevel(ctx)
	}

	return sum / float64(len(skills))
}

// Roll generates a uniform roll and handles side effects.
// Returns a value in [MinValue, 10^(precomputedEffective/10)] for comparison.
// Updates LastUsedAt, LastBase for all skills and applies learning if object has Learning enabled.
// If rng is nil, generates one internally using multiSkillRng.
// If rng is provided, uses it (for static challenges needing multiple rolls from same sequence).
// IMPORTANT: Caller must hold write access to the object (modifies Skills).
func (o *Object) Roll(ctx Context, skills map[string]bool, target string, precomputedEffective, opposingEffective float64, rng *rnd.Rand) float64 {
	if rng == nil {
		rng = multiSkillRng(ctx, skills, o.Unsafe.Id, target)
	}

	at := Stamp(ctx.Now())
	maxRoll := math.Pow(10, precomputedEffective/10)
	roll := math.Max(MinValue, rng.Float64()*maxRoll)

	// Update each skill's state and apply learning
	for name := range skills {
		skill := o.Unsafe.Skills[name]
		if skill.Name == "" {
			skill.Name = name
		}

		// Apply learning BEFORE updating LastUsedAt (learning uses time since last use)
		// Recovery always happens (compensates for forgetting decay)
		// Growth only happens if Learning is enabled
		recovery, growth := skill.learningGains(ctx, opposingEffective)
		skill.Practical += float32(recovery)
		if o.Unsafe.Learning {
			skill.Practical += float32(growth)
			skill.Theoretical += float32(growth)
		}

		// Update state after learning
		rechargeCoeff := skill.rechargeCoeff(ctx)
		skill.LastBase = float32(rechargeCoeff)
		skill.LastUsedAt = at.Uint64()

		o.Unsafe.Skills[name] = skill
	}

	return roll
}

// Filter returns a copy with only descriptions and exits the viewer can perceive.
// Multiple descriptions may be included if the viewer overcomes challenges for multiple descriptions.
func (o *Object) Filter(ctx Context, viewer *Object) (*Object, error) {
	cpy, err := Clone(o)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	cpy.Unsafe.Descriptions = Descriptions(cpy.Unsafe.Descriptions).Detect(ctx, viewer, cpy.Unsafe.Id)

	exits := Exits{}
	for _, exit := range cpy.Unsafe.Exits {
		exitDescs := Descriptions(exit.Descriptions).Detect(ctx, viewer, cpy.Unsafe.Id)
		if len(exitDescs) > 0 {
			exit.Descriptions = exitDescs
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

func (l *Location) AddDescriptionChallenge(added Challenge) (*Location, error) {
	if !added.HasChallenge() {
		return l, nil
	}
	result := &Location{
		Content: Content{},
	}
	var err error
	result.Container, err = l.Container.AddDescriptionChallenge(added)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	for id := range l.Content {
		if result.Content[id], err = l.Content[id].AddDescriptionChallenge(added); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}

	return result, nil
}

func (l *Location) Filter(ctx Context, viewer *Object) (*Location, error) {
	result := &Location{
		Content: Content{},
	}
	for id := range l.Content {
		if id == viewer.GetId() {
			result.Content[id] = l.Content[id]
		} else if cont, err := l.Content[id].Filter(ctx, viewer); err != nil {
			return nil, juicemud.WithStack(err)
		} else if len(cont.GetDescriptions()) > 0 {
			result.Content[id] = cont
		}
	}
	var err error
	if result.Container, err = l.Container.Filter(ctx, viewer); err != nil {
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

// Identify finds an object by pattern match. Supports "N.pattern" to select Nth match.
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

// Perspective describes a location from an observer's point of view.
// Here=true, Exit=nil: observer's current room
// Here=false, Exit!=nil: via that exit
// Here=false, Exit=nil: from an unknown direction (no return exit found)
type Perspective struct {
	Here bool
	Exit *Exit
}

// LocationEmit is the content of events emitted via emitToLocation.
// It wraps the original data with perspective information.
type LocationEmit struct {
	Data        any
	Perspective Perspective
}

// Observers yields each object in the location that passes the given challenge.
// Objects with the same ID as targetID are excluded (you can't observe yourself).
func (l *Location) Observers(ctx Context, targetID string, challenge Challenge) iter.Seq[*Object] {
	return func(yield func(*Object) bool) {
		for viewer := range l.All() {
			if viewer.GetId() != targetID {
				// No challenge = always pass, or check the challenge
				if !challenge.HasChallenge() || challenge.Check(ctx, viewer, targetID, nil) > 0 {
					if !yield(viewer) {
						return
					}
				}
			}
		}
	}
}

// Observation represents an observer and the challenge they overcame to perceive.
type Observation struct {
	Subject     *Object     // The observer
	Challenge   Challenge   // Combined challenge: baseChallenge merged with TransmitChallenge for neighbours
	Perspective Perspective // Where the event came from (observer's perspective)
}

// Observers yields observations for all objects that can perceive targetID.
// For observers in the same location, Challenge = baseChallenge.
// For observers in neighbouring locations, Challenge = baseChallenge merged with exit.TransmitChallenge.
// Perspective indicates where the observation is from (here, via exit, or unknown).
func (n *DeepNeighbourhood) Observers(ctx Context, targetID string, baseChallenge Challenge) iter.Seq[*Observation] {
	return func(yield func(*Observation) bool) {
		// Observers in the same location - perspective is "here"
		for viewer := range n.Location.Observers(ctx, targetID, baseChallenge) {
			if !yield(&Observation{
				Subject:     viewer,
				Challenge:   baseChallenge,
				Perspective: Perspective{Here: true},
			}) {
				return
			}
		}
		// Observers in neighbouring locations, via exits from source
		for _, exit := range n.Location.Container.GetExits() {
			neighbour, found := n.Neighbours[exit.Destination]
			if !found {
				continue
			}
			// Combine base challenge with exit's TransmitChallenge
			combined := baseChallenge.Merge(exit.TransmitChallenge)
			// Find the return exit (observer→source) for perspective
			returnExit := neighbour.Container.FindExit(n.Location.Container.GetId())
			perspective := Perspective{Here: false, Exit: returnExit}
			for viewer := range neighbour.Observers(ctx, targetID, combined) {
				if !yield(&Observation{
					Subject:     viewer,
					Challenge:   combined,
					Perspective: perspective,
				}) {
					return
				}
			}
		}
	}
}

type Detection struct {
	Subject     *Object     // The observer
	Object      *Object     // What the observer perceives (filtered by challenges)
	Perspective Perspective // Where the event came from (observer's perspective)
}

// Detections yields each object that can perceive the target and how it appears to them.
// The perspective describes where the event came from (from the observer's point of view).
// The target should have all challenges applied via AddDescriptionChallenges before calling.
// Typically this includes TransmitChallenges from exits when observing through an exit.
func (l *Location) Detections(ctx Context, target *Object, perspective Perspective) iter.Seq2[*Detection, error] {
	return func(yield func(*Detection, error) bool) {
		for viewer := range l.All() {
			if viewer.GetId() != target.GetId() {
				if filtered, err := target.Filter(ctx, viewer); err != nil {
					if !yield(nil, juicemud.WithStack(err)) {
						return
					}
				} else if len(filtered.GetDescriptions()) > 0 {
					if !yield(&Detection{Subject: viewer, Object: filtered, Perspective: perspective}, nil) {
						return
					}
				}
			}
		}
	}
}

// Detections yields all objects that can perceive the target, including via exits.
// Sensory events travel from source (n.Location) to observers in neighbours.
// TransmitChallenge on the source→neighbour exit is added to the target's description challenge.
// The perspective is set to the observer→source exit (or unknown if no return exit).
func (n *DeepNeighbourhood) Detections(ctx Context, target *Object) iter.Seq2[*Detection, error] {
	return func(yield func(*Detection, error) bool) {
		// Observers in the same location as target - perspective is "here"
		for det, err := range n.Location.Detections(ctx, target, Perspective{Here: true}) {
			if !yield(det, err) {
				return
			}
		}
		// Observers in neighbouring locations, via exits from source
		for _, exit := range n.Location.Container.GetExits() {
			neighbour, found := n.Neighbours[exit.Destination]
			if !found {
				continue
			}
			// Add source→neighbour exit's TransmitChallenge to target
			challenged, err := target.AddDescriptionChallenge(exit.TransmitChallenge)
			if err != nil {
				if !yield(nil, juicemud.WithStack(err)) {
					return
				}
				continue
			}
			// Perspective: observer→source exit (nil if no return exit found)
			returnExit := neighbour.Container.FindExit(n.Location.Container.GetId())
			perspective := Perspective{Here: false, Exit: returnExit}
			for det, err := range neighbour.Detections(ctx, challenged, perspective) {
				if !yield(det, err) {
					return
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

type DeepNeighbourhood struct {
	Location   *Location
	Neighbours map[string]*Location
}

func (n *DeepNeighbourhood) Describe() string {
	b, _ := goccy.MarshalIndent(n, "", "  ")
	return string(b)
}

// Filter returns a copy with only what the viewer can perceive.
func (n *DeepNeighbourhood) Filter(ctx Context, viewer *Object) (*DeepNeighbourhood, error) {
	result := &DeepNeighbourhood{
		Neighbours: map[string]*Location{},
	}

	var err error
	if result.Location, err = n.Location.Filter(ctx, viewer); err != nil {
		return nil, err
	}

	for _, exit := range n.Location.Container.GetExits() {
		if neighbour, found := n.Neighbours[exit.Destination]; found {
			if challenged, err := neighbour.AddDescriptionChallenge(exit.TransmitChallenge); err != nil {
				return nil, juicemud.WithStack(err)
			} else if filtered, err := challenged.Filter(ctx, viewer); err != nil {
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
	// Time for a skill to be fully ready for reuse.
	Recharge SkillDuration
	// Multiplier for success chance when immediately reused.
	Reuse float64
	// Time for skill to be forgotten down to 50% of theoretical level.
	Forget SkillDuration
}

// specificRecharge computes the base recharge coefficient (0-1) using a square curve.
func (s *Skill) specificRecharge(at Timestamp, recharge SkillDuration) float64 {
	if s.LastUsedAt == 0 {
		return 1.0 // Never used = fully recharged
	}
	nanosSinceLastUse := at.Nanoseconds() - Timestamp(s.LastUsedAt).Nanoseconds()
	rechargeFraction := float64(nanosSinceLastUse) / float64(recharge.Nanoseconds())
	// Square curve: recharges fast at the end, at 0.5 gives 0.25, at 1.0 gives 1.0
	return math.Min(1, rechargeFraction*rechargeFraction)
}


// learningGains calculates skill improvement split into recovery and growth.
// Learning uses max(6 minutes, configured Recharge) to prevent speed-learning exploits.
//
// Recovery: Practical catching up to Theoretical ("muscle memory").
// - Always applies when Practical < Theoretical
// - Based on rechargeCoeff and gap size
// - Doesn't depend on challenge difficulty
//
// Growth: Increasing Theoretical ("true learning").
// - Only when Practical ≈ Theoretical (you're at your peak)
// - Only when Theoretical ≈ challengeLevel (appropriate difficulty)
// - Harder at higher levels (diminishing returns)
//
// Returns (recoveryGain, growthGain). Caller should:
// - Add recoveryGain to Practical
// - Add growthGain to both Practical and Theoretical
func (s *Skill) learningGains(ctx Context, challengeLevel float64) (recoveryGain, growthGain float64) {
	// Use same recharge calculation as skill use (includes cumulative Reuse penalty)
	rechargeCoeff := s.rechargeCoeff(ctx)

	// Part 1: Recovery (Practical → Theoretical)
	// Recover proportional to gap, modified by recharge
	gap := float64(s.Theoretical - s.Practical)
	if gap > 0 {
		// Recover 5% of gap per fully-recharged use
		recoveryGain = 0.05 * rechargeCoeff * gap
	}

	// Part 2: Growth (Theoretical increases)
	// Condition 1: Practical must be close to Theoretical (you're "up to speed")
	// Smooth coefficient: 1 when gap=0, 0.5 when gap=1, etc.
	upToSpeedCoeff := 1 / (1 + gap)

	// Condition 2: Theoretical must be close to challenge (appropriate difficulty)
	challengeGap := math.Abs(float64(s.Theoretical) - challengeLevel)
	challengeCoeff := 1 / (1 + challengeGap)

	// Diminishing returns at higher levels
	skillCoeff := 0.0355 * math.Pow(0.9, float64(s.Theoretical))

	growthGain = rechargeCoeff * skillCoeff * upToSpeedCoeff * challengeCoeff

	return recoveryGain, growthGain
}

// practicalPostForget returns the practical level after applying forgetting decay.
// If Forget is 0 (disabled) or skill has never been used (LastUsedAt=0), returns Practical unchanged.
func (s *Skill) practicalPostForget(ctx Context) float64 {
	if s.LastUsedAt == 0 {
		return float64(s.Practical) // Never used, no decay
	}
	cfg := ctx.ServerConfig()
	config := cfg.GetSkillConfig(s.Name)
	if config.Forget == 0 {
		return float64(s.Practical)
	}
	at := Stamp(ctx.Now())
	nanosSinceLastUse := at.Nanoseconds() - Timestamp(s.LastUsedAt).Nanoseconds()
	forgetFraction := float64(nanosSinceLastUse) / float64(config.Forget.Nanoseconds())
	forgetCoeff := 1 + (-1 / (1 + math.Exp(8-8*forgetFraction))) + (1 / math.Exp(8))
	permanentSkill := 0.5 * s.Theoretical
	forgettableSkill := float64(s.Practical - permanentSkill)
	return forgettableSkill*forgetCoeff + float64(permanentSkill)
}

// rechargeCoeff returns the amount of skill level useable considering recharge time.
// If Recharge is 0 (no cooldown), returns 1.0 (fully available).
func (s *Skill) rechargeCoeff(ctx Context) float64 {
	if s.LastUsedAt == 0 {
		return 1.0
	}

	cfg := ctx.ServerConfig()
	sk := cfg.GetSkillConfig(s.Name)
	if sk.Recharge == 0 {
		return 1.0
	}
	at := Stamp(ctx.Now())
	rechargeCoeff := s.specificRecharge(at, sk.Recharge)
	cumulativeReuse := float64(s.LastBase) * sk.Reuse
	return cumulativeReuse + (1-cumulativeReuse)*rechargeCoeff
}

// chargedPracticalLevel returns the skill's practical level with recharge penalty applied.
// effective = Practical + 10 * log10(rechargeCoeff)
// Caller should ensure forgetting has been applied to Practical if needed.
func (s *Skill) chargedPracticalLevel(ctx Context) float64 {
	rechargeCoeff := math.Max(MinValue, s.rechargeCoeff(ctx))
	return float64(s.Practical) + 10*math.Log10(rechargeCoeff)
}

// multiSkillRng returns a deterministic RNG seeded by user, skills, target, and time window.
func multiSkillRng(ctx Context, skills map[string]bool, user, target string) *rnd.Rand {
	skillKey := SkillsKey(skills)

	h := fnv.New64()
	h.Write([]byte(user))
	h.Write([]byte(skillKey))
	h.Write([]byte(target))

	cfg := ctx.ServerConfig()
	at := ctx.Now()

	// Look up duration for this skill combination
	duration := cfg.GetChallengeDuration(skillKey)

	// Seed the hash with time step based on duration.
	step := uint64(at.UnixNano())
	if duration != 0 {
		step = uint64(at.UnixNano() / duration.Nanoseconds() / 3)
	}
	b := make([]byte, binary.Size(step))
	binary.BigEndian.PutUint64(b, step)
	h.Write(b)

	// Use the hash to seed an rng.
	result := rnd.New(rnd.NewSource(int64(h.Sum64())))

	// If there's a duration, reseed with a second step based on a random offset.
	if duration != 0 {
		offset := result.Int63n(duration.Nanoseconds())
		binary.BigEndian.PutUint64(b, uint64((at.UnixNano()+offset)/duration.Nanoseconds()/3))
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
	return strings.Compare(o[i].GetId(), o[j].GetId()) < 0
}

func (o Objects) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}

// Lock sorts objects by ID then locks them in order to prevent deadlocks.
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

// WithLock locks the given objects in consistent order, runs f, then unlocks.
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
