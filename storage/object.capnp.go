// Code generated by capnpc-go. DO NOT EDIT.

package storage

import (
	capnp "capnproto.org/go/capnp/v3"
	text "capnproto.org/go/capnp/v3/encoding/text"
	schemas "capnproto.org/go/capnp/v3/schemas"
)

type Object capnp.Struct

// Object_TypeID is the unique identifier for the type Object.
const Object_TypeID = 0xbc7ab37c3dc9daa6

func NewObject(s *capnp.Segment) (Object, error) {
	st, err := capnp.NewStruct(s, capnp.ObjectSize{DataSize: 0, PointerCount: 6})
	return Object(st), err
}

func NewRootObject(s *capnp.Segment) (Object, error) {
	st, err := capnp.NewRootStruct(s, capnp.ObjectSize{DataSize: 0, PointerCount: 6})
	return Object(st), err
}

func ReadRootObject(msg *capnp.Message) (Object, error) {
	root, err := msg.Root()
	return Object(root.Struct()), err
}

func (s Object) String() string {
	str, _ := text.Marshal(0xbc7ab37c3dc9daa6, capnp.Struct(s))
	return str
}

func (s Object) EncodeAsPtr(seg *capnp.Segment) capnp.Ptr {
	return capnp.Struct(s).EncodeAsPtr(seg)
}

func (Object) DecodeFromPtr(p capnp.Ptr) Object {
	return Object(capnp.Struct{}.DecodeFromPtr(p))
}

func (s Object) ToPtr() capnp.Ptr {
	return capnp.Struct(s).ToPtr()
}
func (s Object) IsValid() bool {
	return capnp.Struct(s).IsValid()
}

func (s Object) Message() *capnp.Message {
	return capnp.Struct(s).Message()
}

func (s Object) Segment() *capnp.Segment {
	return capnp.Struct(s).Segment()
}
func (s Object) Id() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(0)
	return []byte(p.Data()), err
}

func (s Object) HasId() bool {
	return capnp.Struct(s).HasPtr(0)
}

func (s Object) SetId(v []byte) error {
	return capnp.Struct(s).SetData(0, v)
}

func (s Object) Location() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(1)
	return []byte(p.Data()), err
}

func (s Object) HasLocation() bool {
	return capnp.Struct(s).HasPtr(1)
}

func (s Object) SetLocation(v []byte) error {
	return capnp.Struct(s).SetData(1, v)
}

func (s Object) Content() (capnp.DataList, error) {
	p, err := capnp.Struct(s).Ptr(2)
	return capnp.DataList(p.List()), err
}

func (s Object) HasContent() bool {
	return capnp.Struct(s).HasPtr(2)
}

func (s Object) SetContent(v capnp.DataList) error {
	return capnp.Struct(s).SetPtr(2, v.ToPtr())
}

// NewContent sets the content field to a newly
// allocated capnp.DataList, preferring placement in s's segment.
func (s Object) NewContent(n int32) (capnp.DataList, error) {
	l, err := capnp.NewDataList(capnp.Struct(s).Segment(), n)
	if err != nil {
		return capnp.DataList{}, err
	}
	err = capnp.Struct(s).SetPtr(2, l.ToPtr())
	return l, err
}
func (s Object) Subscriptions() (capnp.TextList, error) {
	p, err := capnp.Struct(s).Ptr(3)
	return capnp.TextList(p.List()), err
}

func (s Object) HasSubscriptions() bool {
	return capnp.Struct(s).HasPtr(3)
}

func (s Object) SetSubscriptions(v capnp.TextList) error {
	return capnp.Struct(s).SetPtr(3, v.ToPtr())
}

// NewSubscriptions sets the subscriptions field to a newly
// allocated capnp.TextList, preferring placement in s's segment.
func (s Object) NewSubscriptions(n int32) (capnp.TextList, error) {
	l, err := capnp.NewTextList(capnp.Struct(s).Segment(), n)
	if err != nil {
		return capnp.TextList{}, err
	}
	err = capnp.Struct(s).SetPtr(3, l.ToPtr())
	return l, err
}
func (s Object) State() (string, error) {
	p, err := capnp.Struct(s).Ptr(4)
	return p.Text(), err
}

func (s Object) HasState() bool {
	return capnp.Struct(s).HasPtr(4)
}

func (s Object) StateBytes() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(4)
	return p.TextBytes(), err
}

func (s Object) SetState(v string) error {
	return capnp.Struct(s).SetText(4, v)
}

func (s Object) Source() (string, error) {
	p, err := capnp.Struct(s).Ptr(5)
	return p.Text(), err
}

func (s Object) HasSource() bool {
	return capnp.Struct(s).HasPtr(5)
}

func (s Object) SourceBytes() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(5)
	return p.TextBytes(), err
}

func (s Object) SetSource(v string) error {
	return capnp.Struct(s).SetText(5, v)
}

// Object_List is a list of Object.
type Object_List = capnp.StructList[Object]

// NewObject creates a new list of Object.
func NewObject_List(s *capnp.Segment, sz int32) (Object_List, error) {
	l, err := capnp.NewCompositeList(s, capnp.ObjectSize{DataSize: 0, PointerCount: 6}, sz)
	return capnp.StructList[Object](l), err
}

// Object_Future is a wrapper for a Object promised by a client call.
type Object_Future struct{ *capnp.Future }

func (f Object_Future) Struct() (Object, error) {
	p, err := f.Future.Ptr()
	return Object(p.Struct()), err
}

const schema_d258d93c56221e58 = "x\xda<\xcb\xb1J#Q\x18\xc5\xf1s\xee\x9dI\x9a" +
	"L\xd8\x0f\xa6X\xb6\xd8\x80\xbdB,\x83\xa2X\xda\x98" +
	"\xaf\xd1\xb4\xc9u\x8a\x04\x99\x09\x99\x9bF\x04_\xc2F" +
	"P\x08\x82\xc1\xdeF!`e\x11PPQP\xf1]" +
	"F\x12\x89\xe5\xf9\xfd9\x7f\xc6\x9bA=\xba#\x8c\xc6" +
	"a\xa9\x18\x7fN\xd7\x8f\xae\x0f'\x90\x88E\xeb\xff\xd2" +
	"\xee\xdaG\xeb\x05a\xa9\x0c\xc8\xfd\x85<\x96\x81\xfat" +
	"\x8fX.\xb2N/q~\xc5\xb1\xddO\xfb\x8d\x9dN" +
	"\xaf\x9c8\xdf$\xf5\xaf\x0d\x80\x80\x80\x9c\xfe\x03\xf4\xc4" +
	"RG\x86B\xc6\x9c\xe1\xf96\xa0g\x96ze(\xc6" +
	"\xc44\x80\\n\x01:\xb2\xd4\x89\xa1X\x1b\xd3\x02r" +
	";\x00\xf4\xc6R_\x0d%\x08b\x06\x80<\xaf\x02\xfa" +
	"`\xa9\xef\x86\x12\x861C@\xde\x1a\x80>Y\xea\x97" +
	"\xa1\xed\xee3\x82a\x04\x16\x07\x99k\xfbn\x96\x02X" +
	"\xd8\xb1\xcbR\x9f\xa4\x9eU\xb0i9\xe7*X\xe4\xc3" +
	"N\xee\x06\xdd>j\xb3C\xbe\xc8\x95\x9f\\\xcb}\xdb" +
	"'\xf3U\x017\xf2l8p\xbf\xf3;\x00\x00\xff\xff" +
	"\xa3\x85C@"

func RegisterSchema(reg *schemas.Registry) {
	reg.Register(&schemas.Schema{
		String: schema_d258d93c56221e58,
		Nodes: []uint64{
			0xbc7ab37c3dc9daa6,
		},
		Compressed: true,
	})
}
