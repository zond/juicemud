// Code generated by bencgen golang. DO NOT EDIT.
// source: schema.benc

package structs

import (
    "github.com/deneonet/benc/std"
    "github.com/deneonet/benc/impl/gen"
)

// Struct - Skill
type Skill struct {
    Theoretical float32
    Practical float32
}

// Reserved Ids - Skill
var skillRIds = []uint16{}

// Size - Skill
func (skill *Skill) Size() int {
    return skill.size(0)
}

// Nested Size - Skill
func (skill *Skill) size(id uint16) (s int) {
    s += bstd.SizeFloat32() + 2
    s += bstd.SizeFloat32() + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Skill
func (skill *Skill) SizePlain() (s int) {
    s += bstd.SizeFloat32()
    s += bstd.SizeFloat32()
    return
}

// Marshal - Skill
func (skill *Skill) Marshal(b []byte) {
    skill.marshal(0, b, 0)
}

// Nested Marshal - Skill
func (skill *Skill) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Fixed32, 1)
    n = bstd.MarshalFloat32(n, b, skill.Theoretical)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Fixed32, 2)
    n = bstd.MarshalFloat32(n, b, skill.Practical)

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Skill
func (skill *Skill) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalFloat32(n, b, skill.Theoretical)
    n = bstd.MarshalFloat32(n, b, skill.Practical)
    return n
}

// Unmarshal - Skill
func (skill *Skill) Unmarshal(b []byte) (err error) {
    _, err = skill.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Skill
func (skill *Skill) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, skillRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, skill.Theoretical, err = bstd.UnmarshalFloat32(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, skillRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, skill.Practical, err = bstd.UnmarshalFloat32(n, b); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Skill
func (skill *Skill) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, skill.Theoretical, err = bstd.UnmarshalFloat32(n, b); err != nil {
        return
    }
    if n, skill.Practical, err = bstd.UnmarshalFloat32(n, b); err != nil {
        return
    }
    return
}

// Struct - Challenge
type Challenge struct {
    Skill string
    Level float32
    Message string
}

// Reserved Ids - Challenge
var challengeRIds = []uint16{}

// Size - Challenge
func (challenge *Challenge) Size() int {
    return challenge.size(0)
}

// Nested Size - Challenge
func (challenge *Challenge) size(id uint16) (s int) {
    s += bstd.SizeString(challenge.Skill) + 2
    s += bstd.SizeFloat32() + 2
    s += bstd.SizeString(challenge.Message) + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Challenge
func (challenge *Challenge) SizePlain() (s int) {
    s += bstd.SizeString(challenge.Skill)
    s += bstd.SizeFloat32()
    s += bstd.SizeString(challenge.Message)
    return
}

// Marshal - Challenge
func (challenge *Challenge) Marshal(b []byte) {
    challenge.marshal(0, b, 0)
}

// Nested Marshal - Challenge
func (challenge *Challenge) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 1)
    n = bstd.MarshalString(n, b, challenge.Skill)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Fixed32, 2)
    n = bstd.MarshalFloat32(n, b, challenge.Level)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 3)
    n = bstd.MarshalString(n, b, challenge.Message)

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Challenge
func (challenge *Challenge) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalString(n, b, challenge.Skill)
    n = bstd.MarshalFloat32(n, b, challenge.Level)
    n = bstd.MarshalString(n, b, challenge.Message)
    return n
}

// Unmarshal - Challenge
func (challenge *Challenge) Unmarshal(b []byte) (err error) {
    _, err = challenge.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Challenge
func (challenge *Challenge) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, challengeRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, challenge.Skill, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, challengeRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, challenge.Level, err = bstd.UnmarshalFloat32(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, challengeRIds, 3); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, challenge.Message, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Challenge
func (challenge *Challenge) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, challenge.Skill, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, challenge.Level, err = bstd.UnmarshalFloat32(n, b); err != nil {
        return
    }
    if n, challenge.Message, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    return
}

// Struct - Description
type Description struct {
    Short string
    Long string
    Tags []string
    Challenges []Challenge
}

// Reserved Ids - Description
var descriptionRIds = []uint16{}

// Size - Description
func (description *Description) Size() int {
    return description.size(0)
}

// Nested Size - Description
func (description *Description) size(id uint16) (s int) {
    s += bstd.SizeString(description.Short) + 2
    s += bstd.SizeString(description.Long) + 2
    s += bstd.SizeSlice(description.Tags, bstd.SizeString) + 2
    s += bstd.SizeSlice(description.Challenges, func (s Challenge) int { return s.SizePlain() }) + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Description
func (description *Description) SizePlain() (s int) {
    s += bstd.SizeString(description.Short)
    s += bstd.SizeString(description.Long)
    s += bstd.SizeSlice(description.Tags, bstd.SizeString)
    s += bstd.SizeSlice(description.Challenges, func (s Challenge) int { return s.SizePlain() })
    return
}

// Marshal - Description
func (description *Description) Marshal(b []byte) {
    description.marshal(0, b, 0)
}

// Nested Marshal - Description
func (description *Description) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 1)
    n = bstd.MarshalString(n, b, description.Short)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 2)
    n = bstd.MarshalString(n, b, description.Long)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 3)
    n = bstd.MarshalSlice(n, b, description.Tags, bstd.MarshalString)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 4)
    n = bstd.MarshalSlice(n, b, description.Challenges, func (n int, b []byte, s Challenge) int { return s.MarshalPlain(n, b) })

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Description
func (description *Description) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalString(n, b, description.Short)
    n = bstd.MarshalString(n, b, description.Long)
    n = bstd.MarshalSlice(n, b, description.Tags, bstd.MarshalString)
    n = bstd.MarshalSlice(n, b, description.Challenges, func (n int, b []byte, s Challenge) int { return s.MarshalPlain(n, b) })
    return n
}

// Unmarshal - Description
func (description *Description) Unmarshal(b []byte) (err error) {
    _, err = description.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Description
func (description *Description) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, descriptionRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, description.Short, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, descriptionRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, description.Long, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, descriptionRIds, 3); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, description.Tags, err = bstd.UnmarshalSlice[string](n, b, bstd.UnmarshalString); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, descriptionRIds, 4); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, description.Challenges, err = bstd.UnmarshalSlice[Challenge](n, b, func (n int, b []byte, s *Challenge) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Description
func (description *Description) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, description.Short, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, description.Long, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, description.Tags, err = bstd.UnmarshalSlice[string](n, b, bstd.UnmarshalString); err != nil {
        return
    }
    if n, description.Challenges, err = bstd.UnmarshalSlice[Challenge](n, b, func (n int, b []byte, s *Challenge) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
        return
    }
    return
}

// Struct - Exit
type Exit struct {
    Descriptions []Description
    UseChallenges []Challenge
    TransmitChallenges map[string][]Challenge
    Tags []string
    Destination string
}

// Reserved Ids - Exit
var exitRIds = []uint16{}

// Size - Exit
func (exit *Exit) Size() int {
    return exit.size(0)
}

// Nested Size - Exit
func (exit *Exit) size(id uint16) (s int) {
    s += bstd.SizeSlice(exit.Descriptions, func (s Description) int { return s.SizePlain() }) + 2
    s += bstd.SizeSlice(exit.UseChallenges, func (s Challenge) int { return s.SizePlain() }) + 2
    s += bstd.SizeMap(exit.TransmitChallenges, bstd.SizeString, func (s []Challenge) int { return bstd.SizeSlice(s, func (s Challenge) int { return s.SizePlain() }) }) + 2
    s += bstd.SizeSlice(exit.Tags, bstd.SizeString) + 2
    s += bstd.SizeString(exit.Destination) + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Exit
func (exit *Exit) SizePlain() (s int) {
    s += bstd.SizeSlice(exit.Descriptions, func (s Description) int { return s.SizePlain() })
    s += bstd.SizeSlice(exit.UseChallenges, func (s Challenge) int { return s.SizePlain() })
    s += bstd.SizeMap(exit.TransmitChallenges, bstd.SizeString, func (s []Challenge) int { return bstd.SizeSlice(s, func (s Challenge) int { return s.SizePlain() }) })
    s += bstd.SizeSlice(exit.Tags, bstd.SizeString)
    s += bstd.SizeString(exit.Destination)
    return
}

// Marshal - Exit
func (exit *Exit) Marshal(b []byte) {
    exit.marshal(0, b, 0)
}

// Nested Marshal - Exit
func (exit *Exit) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 1)
    n = bstd.MarshalSlice(n, b, exit.Descriptions, func (n int, b []byte, s Description) int { return s.MarshalPlain(n, b) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 2)
    n = bstd.MarshalSlice(n, b, exit.UseChallenges, func (n int, b []byte, s Challenge) int { return s.MarshalPlain(n, b) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 3)
    n = bstd.MarshalMap(n, b, exit.TransmitChallenges, bstd.MarshalString, func (n int, b []byte, s []Challenge) int { return bstd.MarshalSlice(n, b, s, func (n int, b []byte, s Challenge) int { return s.MarshalPlain(n, b) }) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 4)
    n = bstd.MarshalSlice(n, b, exit.Tags, bstd.MarshalString)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 5)
    n = bstd.MarshalString(n, b, exit.Destination)

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Exit
func (exit *Exit) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalSlice(n, b, exit.Descriptions, func (n int, b []byte, s Description) int { return s.MarshalPlain(n, b) })
    n = bstd.MarshalSlice(n, b, exit.UseChallenges, func (n int, b []byte, s Challenge) int { return s.MarshalPlain(n, b) })
    n = bstd.MarshalMap(n, b, exit.TransmitChallenges, bstd.MarshalString, func (n int, b []byte, s []Challenge) int { return bstd.MarshalSlice(n, b, s, func (n int, b []byte, s Challenge) int { return s.MarshalPlain(n, b) }) })
    n = bstd.MarshalSlice(n, b, exit.Tags, bstd.MarshalString)
    n = bstd.MarshalString(n, b, exit.Destination)
    return n
}

// Unmarshal - Exit
func (exit *Exit) Unmarshal(b []byte) (err error) {
    _, err = exit.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Exit
func (exit *Exit) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, exitRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, exit.Descriptions, err = bstd.UnmarshalSlice[Description](n, b, func (n int, b []byte, s *Description) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, exitRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, exit.UseChallenges, err = bstd.UnmarshalSlice[Challenge](n, b, func (n int, b []byte, s *Challenge) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, exitRIds, 3); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, exit.TransmitChallenges, err = bstd.UnmarshalMap[string, []Challenge](n, b, bstd.UnmarshalString, func (n int, b []byte) (int, []Challenge, error) { return bstd.UnmarshalSlice[Challenge](n, b, func (n int, b []byte, s *Challenge) (int, error) { return s.UnmarshalPlain(n, b) }) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, exitRIds, 4); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, exit.Tags, err = bstd.UnmarshalSlice[string](n, b, bstd.UnmarshalString); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, exitRIds, 5); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, exit.Destination, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Exit
func (exit *Exit) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, exit.Descriptions, err = bstd.UnmarshalSlice[Description](n, b, func (n int, b []byte, s *Description) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
        return
    }
    if n, exit.UseChallenges, err = bstd.UnmarshalSlice[Challenge](n, b, func (n int, b []byte, s *Challenge) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
        return
    }
    if n, exit.TransmitChallenges, err = bstd.UnmarshalMap[string, []Challenge](n, b, bstd.UnmarshalString, func (n int, b []byte) (int, []Challenge, error) { return bstd.UnmarshalSlice[Challenge](n, b, func (n int, b []byte, s *Challenge) (int, error) { return s.UnmarshalPlain(n, b) }) }); err != nil {
        return
    }
    if n, exit.Tags, err = bstd.UnmarshalSlice[string](n, b, bstd.UnmarshalString); err != nil {
        return
    }
    if n, exit.Destination, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    return
}

// Struct - Object
type Object struct {
    Id string
    Callbacks map[string]map[string]bool
    State string
    Location string
    Content map[string]bool
    Skills map[string]Skill
    Descriptions []Description
    Exits []Exit
    SourcePath string
    SourceModTime int64
}

// Reserved Ids - Object
var objectRIds = []uint16{}

// Size - Object
func (object *Object) Size() int {
    return object.size(0)
}

// Nested Size - Object
func (object *Object) size(id uint16) (s int) {
    s += bstd.SizeString(object.Id) + 2
    s += bstd.SizeMap(object.Callbacks, bstd.SizeString, func (s map[string]bool) int { return bstd.SizeMap(s, bstd.SizeString, bstd.SizeBool) }) + 2
    s += bstd.SizeString(object.State) + 2
    s += bstd.SizeString(object.Location) + 2
    s += bstd.SizeMap(object.Content, bstd.SizeString, bstd.SizeBool) + 2
    s += bstd.SizeMap(object.Skills, bstd.SizeString, func (s Skill) int { return s.SizePlain() }) + 2
    s += bstd.SizeSlice(object.Descriptions, func (s Description) int { return s.SizePlain() }) + 2
    s += bstd.SizeSlice(object.Exits, func (s Exit) int { return s.SizePlain() }) + 2
    s += bstd.SizeString(object.SourcePath) + 2
    s += bstd.SizeInt64() + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Object
func (object *Object) SizePlain() (s int) {
    s += bstd.SizeString(object.Id)
    s += bstd.SizeMap(object.Callbacks, bstd.SizeString, func (s map[string]bool) int { return bstd.SizeMap(s, bstd.SizeString, bstd.SizeBool) })
    s += bstd.SizeString(object.State)
    s += bstd.SizeString(object.Location)
    s += bstd.SizeMap(object.Content, bstd.SizeString, bstd.SizeBool)
    s += bstd.SizeMap(object.Skills, bstd.SizeString, func (s Skill) int { return s.SizePlain() })
    s += bstd.SizeSlice(object.Descriptions, func (s Description) int { return s.SizePlain() })
    s += bstd.SizeSlice(object.Exits, func (s Exit) int { return s.SizePlain() })
    s += bstd.SizeString(object.SourcePath)
    s += bstd.SizeInt64()
    return
}

// Marshal - Object
func (object *Object) Marshal(b []byte) {
    object.marshal(0, b, 0)
}

// Nested Marshal - Object
func (object *Object) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 1)
    n = bstd.MarshalString(n, b, object.Id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 2)
    n = bstd.MarshalMap(n, b, object.Callbacks, bstd.MarshalString, func (n int, b []byte, s map[string]bool) int { return bstd.MarshalMap(n, b, s, bstd.MarshalString, bstd.MarshalBool) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 3)
    n = bstd.MarshalString(n, b, object.State)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 4)
    n = bstd.MarshalString(n, b, object.Location)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 5)
    n = bstd.MarshalMap(n, b, object.Content, bstd.MarshalString, bstd.MarshalBool)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 6)
    n = bstd.MarshalMap(n, b, object.Skills, bstd.MarshalString, func (n int, b []byte, s Skill) int { return s.MarshalPlain(n, b) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 7)
    n = bstd.MarshalSlice(n, b, object.Descriptions, func (n int, b []byte, s Description) int { return s.MarshalPlain(n, b) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.ArrayMap, 8)
    n = bstd.MarshalSlice(n, b, object.Exits, func (n int, b []byte, s Exit) int { return s.MarshalPlain(n, b) })
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 9)
    n = bstd.MarshalString(n, b, object.SourcePath)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Fixed64, 10)
    n = bstd.MarshalInt64(n, b, object.SourceModTime)

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Object
func (object *Object) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalString(n, b, object.Id)
    n = bstd.MarshalMap(n, b, object.Callbacks, bstd.MarshalString, func (n int, b []byte, s map[string]bool) int { return bstd.MarshalMap(n, b, s, bstd.MarshalString, bstd.MarshalBool) })
    n = bstd.MarshalString(n, b, object.State)
    n = bstd.MarshalString(n, b, object.Location)
    n = bstd.MarshalMap(n, b, object.Content, bstd.MarshalString, bstd.MarshalBool)
    n = bstd.MarshalMap(n, b, object.Skills, bstd.MarshalString, func (n int, b []byte, s Skill) int { return s.MarshalPlain(n, b) })
    n = bstd.MarshalSlice(n, b, object.Descriptions, func (n int, b []byte, s Description) int { return s.MarshalPlain(n, b) })
    n = bstd.MarshalSlice(n, b, object.Exits, func (n int, b []byte, s Exit) int { return s.MarshalPlain(n, b) })
    n = bstd.MarshalString(n, b, object.SourcePath)
    n = bstd.MarshalInt64(n, b, object.SourceModTime)
    return n
}

// Unmarshal - Object
func (object *Object) Unmarshal(b []byte) (err error) {
    _, err = object.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Object
func (object *Object) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Id, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Callbacks, err = bstd.UnmarshalMap[string, map[string]bool](n, b, bstd.UnmarshalString, func (n int, b []byte) (int, map[string]bool, error) { return bstd.UnmarshalMap[string, bool](n, b, bstd.UnmarshalString, bstd.UnmarshalBool) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 3); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.State, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 4); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Location, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 5); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Content, err = bstd.UnmarshalMap[string, bool](n, b, bstd.UnmarshalString, bstd.UnmarshalBool); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 6); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Skills, err = bstd.UnmarshalMap[string, Skill](n, b, bstd.UnmarshalString, func (n int, b []byte, s *Skill) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 7); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Descriptions, err = bstd.UnmarshalSlice[Description](n, b, func (n int, b []byte, s *Description) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 8); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.Exits, err = bstd.UnmarshalSlice[Exit](n, b, func (n int, b []byte, s *Exit) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 9); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.SourcePath, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, objectRIds, 10); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, object.SourceModTime, err = bstd.UnmarshalInt64(n, b); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Object
func (object *Object) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, object.Id, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, object.Callbacks, err = bstd.UnmarshalMap[string, map[string]bool](n, b, bstd.UnmarshalString, func (n int, b []byte) (int, map[string]bool, error) { return bstd.UnmarshalMap[string, bool](n, b, bstd.UnmarshalString, bstd.UnmarshalBool) }); err != nil {
        return
    }
    if n, object.State, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, object.Location, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, object.Content, err = bstd.UnmarshalMap[string, bool](n, b, bstd.UnmarshalString, bstd.UnmarshalBool); err != nil {
        return
    }
    if n, object.Skills, err = bstd.UnmarshalMap[string, Skill](n, b, bstd.UnmarshalString, func (n int, b []byte, s *Skill) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
        return
    }
    if n, object.Descriptions, err = bstd.UnmarshalSlice[Description](n, b, func (n int, b []byte, s *Description) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
        return
    }
    if n, object.Exits, err = bstd.UnmarshalSlice[Exit](n, b, func (n int, b []byte, s *Exit) (int, error) { return s.UnmarshalPlain(n, b) }); err != nil {
        return
    }
    if n, object.SourcePath, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, object.SourceModTime, err = bstd.UnmarshalInt64(n, b); err != nil {
        return
    }
    return
}

// Struct - Call
type Call struct {
    Name string
    Message string
    Tag string
}

// Reserved Ids - Call
var callRIds = []uint16{}

// Size - Call
func (call *Call) Size() int {
    return call.size(0)
}

// Nested Size - Call
func (call *Call) size(id uint16) (s int) {
    s += bstd.SizeString(call.Name) + 2
    s += bstd.SizeString(call.Message) + 2
    s += bstd.SizeString(call.Tag) + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Call
func (call *Call) SizePlain() (s int) {
    s += bstd.SizeString(call.Name)
    s += bstd.SizeString(call.Message)
    s += bstd.SizeString(call.Tag)
    return
}

// Marshal - Call
func (call *Call) Marshal(b []byte) {
    call.marshal(0, b, 0)
}

// Nested Marshal - Call
func (call *Call) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 1)
    n = bstd.MarshalString(n, b, call.Name)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 2)
    n = bstd.MarshalString(n, b, call.Message)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 3)
    n = bstd.MarshalString(n, b, call.Tag)

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Call
func (call *Call) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalString(n, b, call.Name)
    n = bstd.MarshalString(n, b, call.Message)
    n = bstd.MarshalString(n, b, call.Tag)
    return n
}

// Unmarshal - Call
func (call *Call) Unmarshal(b []byte) (err error) {
    _, err = call.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Call
func (call *Call) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, callRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, call.Name, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, callRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, call.Message, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, callRIds, 3); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, call.Tag, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Call
func (call *Call) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, call.Name, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, call.Message, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, call.Tag, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    return
}

// Struct - Event
type Event struct {
    At uint64
    Object string
    Call Call
    Key string
}

// Reserved Ids - Event
var eventRIds = []uint16{}

// Size - Event
func (event *Event) Size() int {
    return event.size(0)
}

// Nested Size - Event
func (event *Event) size(id uint16) (s int) {
    s += bstd.SizeUint64() + 2
    s += bstd.SizeString(event.Object) + 2
    s += event.Call.size(3)
    s += bstd.SizeString(event.Key) + 2

    if id > 255 {
        s += 5
        return
    }
    s += 4
    return
}

// SizePlain - Event
func (event *Event) SizePlain() (s int) {
    s += bstd.SizeUint64()
    s += bstd.SizeString(event.Object)
    s += event.Call.SizePlain()
    s += bstd.SizeString(event.Key)
    return
}

// Marshal - Event
func (event *Event) Marshal(b []byte) {
    event.marshal(0, b, 0)
}

// Nested Marshal - Event
func (event *Event) marshal(tn int, b []byte, id uint16) (n int) {
    n = bgenimpl.MarshalTag(tn, b, bgenimpl.Container, id)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Fixed64, 1)
    n = bstd.MarshalUint64(n, b, event.At)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 2)
    n = bstd.MarshalString(n, b, event.Object)
    n = event.Call.marshal(n, b, 3)
    n = bgenimpl.MarshalTag(n, b, bgenimpl.Bytes, 4)
    n = bstd.MarshalString(n, b, event.Key)

    n += 2
    b[n-2] = 1
    b[n-1] = 1
    return
}

// MarshalPlain - Event
func (event *Event) MarshalPlain(tn int, b []byte) (n int) {
    n = tn
    n = bstd.MarshalUint64(n, b, event.At)
    n = bstd.MarshalString(n, b, event.Object)
    n = event.Call.MarshalPlain(n, b)
    n = bstd.MarshalString(n, b, event.Key)
    return n
}

// Unmarshal - Event
func (event *Event) Unmarshal(b []byte) (err error) {
    _, err = event.unmarshal(0, b, []uint16{}, 0)
    return
}

// Nested Unmarshal - Event
func (event *Event) unmarshal(tn int, b []byte, r []uint16, id uint16) (n int, err error) {
    var ok bool
    if n, ok, err = bgenimpl.HandleCompatibility(tn, b, r, id); !ok {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, eventRIds, 1); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, event.At, err = bstd.UnmarshalUint64(n, b); err != nil {
            return
        }
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, eventRIds, 2); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, event.Object, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    if n, err = event.Call.unmarshal(n, b, eventRIds, 3); err != nil {
        return
    }
    if n, ok, err = bgenimpl.HandleCompatibility(n, b, eventRIds, 4); err != nil {
        if err == bgenimpl.ErrEof {
            return n, nil
        }
        return
    }
    if ok {
        if n, event.Key, err = bstd.UnmarshalString(n, b); err != nil {
            return
        }
    }
    n += 2
    return
}

// UnmarshalPlain - Event
func (event *Event) UnmarshalPlain(tn int, b []byte) (n int, err error) {
    n = tn
    if n, event.At, err = bstd.UnmarshalUint64(n, b); err != nil {
        return
    }
    if n, event.Object, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    if n, err = event.Call.UnmarshalPlain(n, b); err != nil {
        return
    }
    if n, event.Key, err = bstd.UnmarshalString(n, b); err != nil {
        return
    }
    return
}

