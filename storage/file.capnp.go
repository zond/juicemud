// Code generated by capnpc-go. DO NOT EDIT.

package storage

import (
	capnp "capnproto.org/go/capnp/v3"
	text "capnproto.org/go/capnp/v3/encoding/text"
)

type File capnp.Struct

// File_TypeID is the unique identifier for the type File.
const File_TypeID = 0xa69f1d8ec5dd9152

func NewFile(s *capnp.Segment) (File, error) {
	st, err := capnp.NewStruct(s, capnp.ObjectSize{DataSize: 8, PointerCount: 3})
	return File(st), err
}

func NewRootFile(s *capnp.Segment) (File, error) {
	st, err := capnp.NewRootStruct(s, capnp.ObjectSize{DataSize: 8, PointerCount: 3})
	return File(st), err
}

func ReadRootFile(msg *capnp.Message) (File, error) {
	root, err := msg.Root()
	return File(root.Struct()), err
}

func (s File) String() string {
	str, _ := text.Marshal(0xa69f1d8ec5dd9152, capnp.Struct(s))
	return str
}

func (s File) EncodeAsPtr(seg *capnp.Segment) capnp.Ptr {
	return capnp.Struct(s).EncodeAsPtr(seg)
}

func (File) DecodeFromPtr(p capnp.Ptr) File {
	return File(capnp.Struct{}.DecodeFromPtr(p))
}

func (s File) ToPtr() capnp.Ptr {
	return capnp.Struct(s).ToPtr()
}
func (s File) IsValid() bool {
	return capnp.Struct(s).IsValid()
}

func (s File) Message() *capnp.Message {
	return capnp.Struct(s).Message()
}

func (s File) Segment() *capnp.Segment {
	return capnp.Struct(s).Segment()
}
func (s File) Path() (string, error) {
	p, err := capnp.Struct(s).Ptr(0)
	return p.Text(), err
}

func (s File) HasPath() bool {
	return capnp.Struct(s).HasPtr(0)
}

func (s File) PathBytes() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(0)
	return p.TextBytes(), err
}

func (s File) SetPath(v string) error {
	return capnp.Struct(s).SetText(0, v)
}

func (s File) Dir() bool {
	return capnp.Struct(s).Bit(0)
}

func (s File) SetDir(v bool) {
	capnp.Struct(s).SetBit(0, v)
}

func (s File) ReadGroup() (string, error) {
	p, err := capnp.Struct(s).Ptr(1)
	return p.Text(), err
}

func (s File) HasReadGroup() bool {
	return capnp.Struct(s).HasPtr(1)
}

func (s File) ReadGroupBytes() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(1)
	return p.TextBytes(), err
}

func (s File) SetReadGroup(v string) error {
	return capnp.Struct(s).SetText(1, v)
}

func (s File) WriteGroup() (string, error) {
	p, err := capnp.Struct(s).Ptr(2)
	return p.Text(), err
}

func (s File) HasWriteGroup() bool {
	return capnp.Struct(s).HasPtr(2)
}

func (s File) WriteGroupBytes() ([]byte, error) {
	p, err := capnp.Struct(s).Ptr(2)
	return p.TextBytes(), err
}

func (s File) SetWriteGroup(v string) error {
	return capnp.Struct(s).SetText(2, v)
}

// File_List is a list of File.
type File_List = capnp.StructList[File]

// NewFile creates a new list of File.
func NewFile_List(s *capnp.Segment, sz int32) (File_List, error) {
	l, err := capnp.NewCompositeList(s, capnp.ObjectSize{DataSize: 8, PointerCount: 3}, sz)
	return capnp.StructList[File](l), err
}

// File_Future is a wrapper for a File promised by a client call.
type File_Future struct{ *capnp.Future }

func (f File_Future) Struct() (File, error) {
	p, err := f.Future.Ptr()
	return File(p.Struct()), err
}