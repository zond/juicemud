//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
package structs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"iter"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/game/skills"
	"github.com/zond/juicemud/lang"

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
	if len(o.Descriptions) == 0 {
		return "nameless"
	}
	return o.Descriptions[0].Short
}

func (o *Object) Unique() bool {
	if len(o.Descriptions) == 0 {
		return false
	}
	return o.Descriptions[0].Unique
}

func (o *Object) Indef() string {
	name := o.Name()
	if o.Unique() {
		return name
	}
	return lang.Indef(name)
}

func (o *Object) HasCallback(name string, tag string) bool {
	tags, found := o.Callbacks[name]
	if !found {
		return false
	}
	_, found = tags[tag]
	return found
}

func MakeObject(ctx context.Context) (*Object, error) {
	object := &Object{
		Callbacks: map[string]map[string]bool{},
		Content:   map[string]bool{},
		Skills:    map[string]Skill{},
	}
	newID, err := NextObjectID()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	object.Id = newID
	return object, nil
}

func (e *Event) CreateKey() {
	eventCounter := juicemud.Increment(&lastEventCounter)
	atSize := binary.Size(e.At)
	k := make([]byte, atSize+binary.Size(eventCounter))
	binary.BigEndian.PutUint64(k, uint64(e.At))
	binary.BigEndian.PutUint64(k[atSize:], eventCounter)
	e.Key = string(k)
}

// Check returns whether the challenger succeeds with the challenges,
// considering the target.
func (c *Challenge) Check(challenger *Object, targetID string) bool {
	return skills.Application{
		Use: skills.Use{
			User:  challenger.Id,
			Skill: c.Skill,
			At:    time.Now(),
		},
		Target:    targetID,
		Level:     challenger.Skills[c.Skill].Practical,
		Challenge: c.Level,
	}.Check()
}

type Challenges []Challenge

func (c *Challenges) Merge(mergeChallenges map[string]Challenge) {
	newChallenges := Challenges{}
	for idx := range *c {
		challenge := (*c)[idx]
		if mergeChallenge, found := mergeChallenges[challenge.Skill]; found {
			challenge.Level += mergeChallenge.Level
			newChallenges = append(newChallenges, challenge)
		} else {
			newChallenges = append(newChallenges, mergeChallenge)
		}
	}
	*c = newChallenges
}

func (c Challenges) Map() map[string]Challenge {
	result := map[string]Challenge{}
	for _, challenge := range c {
		result[challenge.Skill] = challenge
	}
	return result
}

// Check returns whether the challenger succeeds with all challenges,
// considering the target.
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

// Detect will return the first detected description.
func (d Descriptions) Detect(viewer *Object, targetID string) *Description {
	for _, desc := range d {
		if Challenges(desc.Challenges).Check(viewer, targetID) {
			return &desc
		}
	}
	return nil
}

// AddDescriptionChallenges will merge the addedChallenges into all descriptions
// of the object using the skill name as key.
func (o *Object) AddDescriptionChallenges(addedChallenges Challenges) {
	mergeChallenges := addedChallenges.Map()
	for currDescIdx := range o.Descriptions {
		(*Challenges)(&o.Descriptions[currDescIdx].Challenges).Merge(mergeChallenges)
	}
}

// Filter will remove all undetected descriptions,
// and all undetected descriptions of exits, and remove all exits
// that lacks descriptions.
func (o *Object) Filter(viewer *Object) {
	if desc := Descriptions(o.Descriptions).Detect(viewer, o.Id); desc != nil {
		o.Descriptions = []Description{*desc}
	} else {
		o.Descriptions = nil
	}
	exits := Exits{}
	for _, exit := range o.Exits {
		if exitDesc := Descriptions(exit.Descriptions).Detect(viewer, o.Id); exitDesc != nil {
			exit.Descriptions = []Description{*exitDesc}
			exits = append(exits, exit)
		}
	}
	o.Exits = exits
}

func (o *Object) Describe() string {
	b, _ := goccy.MarshalIndent(o, "", "  ")
	return string(b)
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

type Location struct {
	Container *Object
	Content   Content
}

func (l *Location) Describe() string {
	b, _ := goccy.MarshalIndent(l, "", "  ")
	return string(b)
}

// AddDescriptionChallenges will merge the addedChalls into the container and the content.
func (l *Location) AddDescriptionChallenges(challenges Challenges) {
	l.Container.AddDescriptionChallenges(challenges)
	for _, content := range l.Content {
		content.AddDescriptionChallenges(challenges)
	}
}

// Filter will remove all undetected descriptions of the container,
// and all undetected descriptions of content that isn't the viewer,
// and remove the content that lacks descriptions.
func (l *Location) Filter(viewer *Object) {
	content := Content{}
	for id, cont := range l.Content {
		if id == viewer.Id {
			content[id] = cont
		} else {
			cont.Filter(viewer)
			if len(cont.Descriptions) > 0 {
				content[id] = cont
			}
		}
	}
	l.Content = content
	l.Container.Filter(viewer)
}

func (l *Location) All() iter.Seq2[string, *Object] {
	return func(yield func(string, *Object) bool) {
		if !yield(l.Container.Id, l.Container) {
			return
		}
		for k, v := range l.Content {
			if !yield(k, v) {
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
	if Descriptions(l.Container.Descriptions).Matches(pattern) {
		objs = append(objs, l.Container)
	}
	for _, cont := range l.Content {
		if Descriptions(cont.Descriptions).Matches(pattern) {
			objs = append(objs, cont)
		}
	}
	if len(objs) == 0 {
		return nil, errors.Errorf("No %q found", pattern)
	}
	if len(objs) == 1 && (index == 0 || index == -1) {
		return objs[0], nil
	}
	if index == -1 {
		return nil, errors.Errorf("%v %q found, pick one", len(objs), pattern)
	}
	if index < len(objs) {
		return objs[index], nil
	}
	return nil, errors.Errorf("Only %v %q found", len(objs), pattern)
}

type Detection struct {
	Subject *Object
	Object  *Object
}

// Detections yields each detection event of target by container and content, considering added challenges.
func (l *Location) Detections(target *Object, addedChallenges Challenges) iter.Seq2[*Detection, error] {
	return func(yield func(*Detection, error) bool) {
		for _, viewer := range l.All() {
			if viewer.Id != target.Id {
				clone, err := Clone(target)
				if err != nil {
					yield(nil, juicemud.WithStack(err))
				} else {
					clone.AddDescriptionChallenges(addedChallenges)
					clone.Filter(viewer)
					if len(clone.Descriptions) > 0 {
						yield(&Detection{Subject: viewer, Object: clone}, nil)
					}
				}
			}
		}
	}
}

// Detections yeilds each detection event of target in the location, and in all neighbours - with neighbour-exit-to-location
// transmit challenges taken into account.
func (n *DeepNeighbourhood) Detections(target *Object) iter.Seq2[*Detection, error] {
	return func(yield func(*Detection, error) bool) {
		for det, err := range n.Location.Detections(target, nil) {
			yield(det, err)
		}
		for _, neighbour := range n.Neighbours {
			for _, exit := range neighbour.Container.Exits {
				if exit.Destination == n.Location.Container.Id {
					for det, err := range neighbour.Detections(target, Challenges(exit.TransmitChallenges)) {
						yield(det, err)
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

// FindLocation returns the path leading to locID in this neighbourhood, and whether it was found.
// Empty path means the locID is the center of the neighbourhood (Neighbourhood.Location).
func (n *Neighbourhood) FindLocation(locID string) (*Exit, bool) {
	if n.Location.Id == locID {
		return nil, true
	}
	for _, exit := range n.Location.Exits {
		if neigh, found := n.Neighbours[exit.Destination]; found && neigh.Id == locID {
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

// Filter will filter the location for the viewer, then all neighbours that still have exits.
// The neighbours will also be filtered after the exit challenges are added, and any neighbours
// without descriptions will not be added.
func (n *DeepNeighbourhood) Filter(viewer *Object) {
	n.Location.Filter(viewer)
	neighbours := map[string]*Location{}
	for _, exit := range n.Location.Container.Exits {
		neighbour := n.Neighbours[exit.Destination]
		neighbour.AddDescriptionChallenges(exit.TransmitChallenges)
		neighbour.Filter(viewer)
		if len(neighbour.Container.Descriptions) > 0 {
			neighbours[exit.Destination] = n.Neighbours[exit.Destination]
		}
	}
	n.Neighbours = neighbours
}

func (n *DeepNeighbourhood) All() iter.Seq2[string, *Object] {
	return func(yield func(string, *Object) bool) {
		for k, v := range n.Location.All() {
			if !yield(k, v) {
				return
			}
		}
		for _, loc := range n.Neighbours {
			for k, v := range loc.All() {
				if !yield(k, v) {
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
