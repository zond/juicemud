//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
package structs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
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

func (o *Object) Describe(viewer *Object) []*Description {
	result := []*Description{}
	for _, desc := range o.Descriptions {
		if func() bool {
			for _, challenge := range desc.Challenges {
				if !challenge.Check(viewer, o) {
					return false
				}
			}
			return true
		}() {
			result = append(result, &desc)
		}
	}
	return result
}
