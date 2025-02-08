//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
package structs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"iter"
	"strings"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/game/skills"
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

// Detect will return the first detected description.
func (d Descriptions) Detect(viewer *Object, targetID string) *Description {
	for _, desc := range d {
		if Challenges(desc.Challenges).Check(viewer, targetID) {
			return &desc
		}
	}
	return nil
}

// AddDescriptionChallenges will merge the addedChalls into all descriptions
// of the object using the skill name as key.
func (o *Object) AddDescriptionChallenges(addedChalls Challenges) {
	addedChallMap := addedChalls.Map()
	for currDescIdx := range o.Descriptions {
		currDesc := &o.Descriptions[currDescIdx]
		replChalls := Challenges{}
		for currChallIdx := range o.Descriptions[currDescIdx].Challenges {
			currChall := currDesc.Challenges[currChallIdx]
			if addedChall, found := addedChallMap[currChall.Skill]; found {
				currChall.Level += addedChall.Level
				replChalls = append(replChalls, currChall)
			} else {
				replChalls = append(replChalls, addedChall)
			}
		}
		currDesc.Challenges = replChalls
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

type Exits []Exit

func (e Exits) Short() string {
	result := []string{}
	for _, exit := range e {
		if len(exit.Descriptions) == 0 {
			continue
		}
		result = append(result, exit.Descriptions[0].Short)
	}
	return strings.Join(result, ", ")
}

type Content map[string]*Object

func (c Content) Short() []string {
	result := make([]string, 0, len(c))
	for _, obj := range c {
		result = append(result, obj.Descriptions[0].Short)
	}
	return result
}

type Location struct {
	Container *Object
	Content   Content
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

type Neighbourhood struct {
	Location   *Location
	Neighbours map[string]*Location
}

// Filter will filter the location for the viewer, then all neighbours that still have exits.
// The neighbours will also be filtered after the exit challenges are added, and any neighbours
// without descriptions will not be added.
func (n *Neighbourhood) Filter(viewer *Object) {
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

func (n *Neighbourhood) All() iter.Seq2[string, *Object] {
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
