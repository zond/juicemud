package structs

import (
	"context"
	"crypto/rand"
	"encoding/binary"

	"github.com/zond/juicemud"

	goccy "github.com/goccy/go-json"
)

var (
	lastObjectCounter uint64 = 0
)

const (
	objectIDLen = 16
)

func NextObjectID() ([]byte, error) {
	objectCounter := juicemud.Increment(&lastObjectCounter)
	timeSize := binary.Size(objectCounter)
	result := make([]byte, objectIDLen)
	binary.BigEndian.PutUint64(result, objectCounter)
	if _, err := rand.Read(result[timeSize:]); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
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
	Destination     []byte
}

type ByteString string

func (bs ByteString) MarshalText() (text []byte, err error) {
	return goccy.Marshal([]byte(bs))
}

func (bs *ByteString) UnmarshalText(text []byte) error {
	b := []byte{}
	if err := goccy.Unmarshal(text, &b); err != nil {
		return err
	}
	*bs = ByteString(b)
	return nil
}

type Object struct {
	Id        []byte
	Callbacks map[string]map[string]bool // map[event_type]map[tag]bool where tag is e.g. command or event.
	State     string

	Location     []byte
	Content      map[ByteString]bool `faker:"ByteStringMap"`
	Skills       map[string]Skill
	Descriptions []Description
	Exits        []Exit
	SourcePath   string
}

func MakeObject(ctx context.Context) (*Object, error) {
	object := &Object{
		Callbacks: map[string]map[string]bool{},
		Content:   map[ByteString]bool{},
		Skills:    map[string]Skill{},
	}
	newID, err := NextObjectID()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	object.Id = newID
	return object, nil
}
