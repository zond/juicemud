package structs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"

	"github.com/zond/juicemud"
)

var (
	lastObjectCounter uint64 = 0
	encoding                 = base64.StdEncoding.WithPadding(base64.NoPadding)
)

const (
	objectIDLen = 16
)

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

type Skill struct {
	Theoretical float32
	Practical   float32
}

type Challenge struct {
	Skill string
	Level float32
}

type Description struct {
	Short      string
	Long       string
	Tags       []string
	Challenges []Challenge
}

type Exit struct {
	Descriptions    []Description
	UseChallenges   []Challenge
	LookChallenges  []Challenge
	SniffChallenges []Challenge
	HearChallenges  []Challenge
	Destination     string
}

type Object struct {
	Id        string
	Callbacks map[string]map[string]bool // map[event_type]map[tag]bool where tag is e.g. command or event.
	State     string

	Location     string
	Content      map[string]bool
	Skills       map[string]Skill
	Descriptions []Description
	Exits        []Exit
	SourcePath   string
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
