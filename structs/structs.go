//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
package structs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
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

func (c *Challenge) Check(challenger *Object, target *Object) bool {
	return skills.Application{
		Use: skills.Use{
			User:  challenger.Id,
			Skill: c.Skill,
			At:    time.Now(),
		},
		Target:    target.Id,
		Level:     challenger.Skills[c.Skill].Practical,
		Challenge: c.Level,
	}.Check()
}

type Descriptions []Description

func (d Descriptions) Detect(target *Object, viewer *Object) *Description {
	for _, desc := range d {
		if func() bool {
			for _, challenge := range desc.Challenges {
				if !challenge.Check(viewer, target) {
					return false
				}
			}
			return true
		}() {
			return &desc
		}
	}
	return nil
}

type Objects []Object

func (o Objects) Short() []string {
	result := make([]string, len(o))
	for i := range o {
		result[i] = o[i].Descriptions[0].Short
	}
	return result
}

func (o *Object) Inspect(viewer *Object) (*Description, Exits) {
	desc := Descriptions(o.Descriptions).Detect(o, viewer)
	exits := Exits{}
	for _, exit := range o.Exits {
		if exitDesc := Descriptions(exit.Descriptions).Detect(o, viewer); exitDesc != nil {
			exit.Descriptions = []Description{*exitDesc}
			exits = append(exits, exit)
		}
	}
	return desc, exits
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
