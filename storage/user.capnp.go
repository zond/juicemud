// Code generated by capnpc-go. DO NOT EDIT.

package storage

import (
	capnp "capnproto.org/go/capnp/v3"
	text "capnproto.org/go/capnp/v3/encoding/text"
	schemas "capnproto.org/go/capnp/v3/schemas"
)

type User capnp.Struct

// User_TypeID is the unique identifier for the type User.
const User_TypeID = 0x84b4c0160acf3f04

func NewUser(s *capnp.Segment) (User, error) {
	st, err := capnp.NewStruct(s, capnp.ObjectSize{DataSize: 8, PointerCount: 2})
	return User(st), err
}

func NewRootUser(s *capnp.Segment) (User, error) {
	st, err := capnp.NewRootStruct(s, capnp.ObjectSize{DataSize: 8, PointerCount: 2})
	return User(st), err
}

func ReadRootUser(msg *capnp.Message) (User, error) {
	root, err := msg.Root()
	return User(root.Struct()), err
}

func (s User) String() string {
	str, _ := text.Marshal(0x84b4c0160acf3f04, capnp.Struct(s))
	return str
}

func (s User) EncodeAsPtr(seg *capnp.Segment) capnp.Ptr {
	return capnp.Struct(s).EncodeAsPtr(seg)
}

func (User) DecodeFromPtr(p capnp.Ptr) User {
	return User(capnp.Struct{}.DecodeFromPtr(p))
}

func (s User) ToPtr() capnp.Ptr {
	return capnp.Struct(s).ToPtr()
}
func (s User) IsValid() bool {
	return capnp.Struct(s).IsValid()
}

func (s User) Message() *capnp.Message {
	return capnp.Struct(s).Message()
}

func (s User) Segment() *capnp.Segment {
	return capnp.Struct(s).Segment()
}
func (s User) Name() (string, error) {
	p, err := capnp.Struct(s).Ptr(0)
	return p.Text(), err
}

func (s User) HasName() bool {
	return capnp.Struct(s).HasPtr(0)
}

func (s User) NameBytes() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(0)
	return p.TextBytes(), err
}

func (s User) SetName(v string) error {
	return capnp.Struct(s).SetText(0, v)
}

func (s User) PasswordHash() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(1)
	return []byte(p.Data()), err
}

func (s User) HasPasswordHash() bool {
	return capnp.Struct(s).HasPtr(1)
}

func (s User) SetPasswordHash(v []byte) error {
	return capnp.Struct(s).SetData(1, v)
}

func (s User) Owner() bool {
	return capnp.Struct(s).Bit(0)
}

func (s User) SetOwner(v bool) {
	capnp.Struct(s).SetBit(0, v)
}

// User_List is a list of User.
type User_List = capnp.StructList[User]

// NewUser creates a new list of User.
func NewUser_List(s *capnp.Segment, sz int32) (User_List, error) {
	l, err := capnp.NewCompositeList(s, capnp.ObjectSize{DataSize: 8, PointerCount: 2}, sz)
	return capnp.StructList[User](l), err
}

// User_Future is a wrapper for a User promised by a client call.
type User_Future struct{ *capnp.Future }

func (f User_Future) Struct() (User, error) {
	p, err := f.Future.Ptr()
	return User(p.Struct()), err
}

const schema_a687d0c53195176c = "x\xda\x8c\x911h\x13o\x18\xc6\xdf\xe7\xfb.\xff\xf4" +
	"\xaf\x09\xe9qE\x1d\xd4BU\xb4\xa2\xa5-\x1dJ\xb1" +
	"\xb4\x8b\xb6bE\xbfb\xa5\x8a\xa0\x97\xcbWs!\xc9" +
	"\x9dw\x17\x03\x12P\xab\xe0\xa6X(\xe2 \xa2\xd8\x80" +
	"\x93\x83\x82\x8b\x83\"\x19\xc4AqSp\xd5\xc9n]" +
	"\\N\xbeK.\xd1\xe0\xe0\xf6\xf2\xf2\xbc\xcf\xfb{\x9f" +
	"w8\xc0\xb46\x92\xbe\xaa\x11\x13c\x89\xffBm\xea" +
	"\xc3\xa6-\xaf_\xdc$\xb1\x19\x08\x8b[WG\x1a\x1f" +
	"o\xd5)\xc1\x92D\xc6\x1e\xb6l\x0c\xb6\xaag\x84p" +
	"\xfe\xee\xd7\xc6\xed\x1d\x0f\xebM\xf1\xe4\x8f\xbd\xbd\xf7\xfc" +
	"\xa5\xfb\x94\xe0J\xf2\x86-\x1b\x0d\xd6\xac\xbe\x13\xc2\x8d" +
	"\xd5\x9e\xf5c\xdb\x92/I7\x10\x8e\xef\xbaQ\x13\xd6" +
	"\xda\xfb\x96\xf1\x03\xfe\xd3x\x1aM\xad\xf1*!\xac\x7f" +
	"y7Y{~\xe5\x15\xe9i\x84\x8b;\x07N\x1f\xfa" +
	"\xbc\xf8\xa9\xa5\x85\xf6\xd8\xf8_SUBS\xda\xeb\xd7" +
	"V\x1e-\x94\xdfn\x90\x9eBx\xe7\xf2\xdc\xf6'\x89" +
	"\xf5o-\xad\xa9\xad\x18v\xa4\x95Z\x95\xce\x84\x15_" +
	"zC\x96\xe9\xa2\xecN,\xf8\xd2\xa3\x93\x80Hq\x8d" +
	"H\x03\x91~x?\x91\x98\xe6\x10s\x0c:\xd0\x07\xd5" +
	"<Z \x12\xb3\x1c\xe2\x14\x03X\x1f\x18\x91.F\x89" +
	"\xc4\x1c\x87Xd\xc8\x94\xcd\x92D\x8a\x18R\x84\xd05" +
	"}\xbf\xeax9\xca\xcc\x9a~\x1eibH\x13\xfa\x9d" +
	"jYz\x001\x80\x10.\xd9E\x19c\x1c\xb1\x8b2" +
	"\xc2\xe8mc\x98\x0a\xe3\x1c\x87\xc83\xc4\x14r\x80H" +
	"\\\xe0\x10E\x06\x9d\xa1\x89a\xcf\x13\x89<\x87\x08\x18" +
	"t\xce\xfa\xc0\x89\xf4Kg\x89\x84\xcb!j\x0c\x19\xd7" +
	"\x0c\xf21[2gw\x10<i\xe6f<\xa7Bp" +
	"\xdb\xecU\xcf\x0e\xe4\x8c\xe7\x10\xaft\x9a\x17=\xa7\xe2" +
	"\x9e/IV\xca6\xb3+\xbb\x13j\xd0\x9d:.U" +
	"K\xa1\xf7\xb4\xd1\x07\x15\xfan\x0e1\xfc[\x82\x07U" +
	"Z\xfb8\xc4\x18CF\xbd 6\xef\x8f\xcc\xdb\xab\x9c" +
	"lAZ\xc1\x90\x85h\xc9\x89l!)\xad\xa0\xcb\x7f" +
	"\xf4o\xfe\x13\x1d\xff~?0\x03\x19\xe7>\xe5;\x15" +
	"\xcf\x92\x7f\x1e3d\x99\x88\xaf\x80\xfb/\xfc*\xd1\x03" +
	"\x1cb\xbc\xfb\xdb\xd1[\xbb\x12\xfb\x15\x00\x00\xff\xff\x0a" +
	"\xc4\xb1?"

func RegisterSchema(reg *schemas.Registry) {
	reg.Register(&schemas.Schema{
		String: schema_a687d0c53195176c,
		Nodes: []uint64{
			0x84b4c0160acf3f04,
			0xa69f1d8ec5dd9152,
			0xb707184bee0895f5,
			0xbc7ab37c3dc9daa6,
			0xf5c36e55a1928081,
		},
		Compressed: true,
	})
}