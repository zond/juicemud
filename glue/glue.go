package glue

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"iter"
	"reflect"
	"strings"

	"capnproto.org/go/capnp/v3"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"rogchap.com/v8go"

	stderrors "errors"
)

type dstType int

const (
	dstUnknown dstType = iota
	dstPrim
	dstStruct
	dstList
)

var (
	capnpListType   = reflect.TypeOf(capnp.ListKind{})
	capnpStructType = reflect.TypeOf(capnp.StructKind{})
	errorType       = reflect.TypeOf((*error)(nil)).Elem()
	intType         = reflect.TypeOf(0)
	byteType        = reflect.TypeOf(byte(0))
)

func castPrim(val *v8go.Value, dstType reflect.Type) (any, error) {
	switch dstType.Kind() {
	case reflect.Int:
		return int(val.Int32()), nil
	case reflect.String:
		return val.String(), nil
	case reflect.Float32:
		return float32(val.Number()), nil
	case reflect.Slice:
		if dstType.Elem() == byteType {
			b, err := hex.DecodeString(val.String())
			if err != nil {
				return nil, juicemud.WithStack(err)
			}
			return b, nil
		}
	}
	return nil, errors.Errorf("%v can't be cast to primitive", dstType)
}

func dstTypeOf(dst reflect.Type) dstType {
	switch dst.Kind() {
	case reflect.Struct:
		if dst.ConvertibleTo(capnpListType) {
			return dstList
		} else if dst.ConvertibleTo(capnpStructType) {
			return dstStruct
		}
		return dstUnknown
	case reflect.Int:
		fallthrough
	case reflect.String:
		fallthrough
	case reflect.Float32:
		fallthrough
	case reflect.Slice:
		return dstPrim
	}
	return dstUnknown
}

func callFunc(fun reflect.Value, resultTypes []reflect.Type, anyArgs ...any) ([]reflect.Value, error) {
	funType := fun.Type()
	if funType.NumIn() != len(anyArgs) {
		return nil, errors.Errorf("%v doesn't take %v arguments", fun, len(anyArgs))
	}
	valArgs := make([]reflect.Value, len(anyArgs))
	for index, anyArg := range anyArgs {
		switch arg := anyArg.(type) {
		case reflect.Value:
			valArgs[index] = arg
		default:
			valArgs[index] = reflect.ValueOf(arg)
		}
	}
	results := fun.Call(valArgs)
	if len(results) == 0 {
		return nil, nil
	}
	var err error
	remainingResults := []reflect.Value{}
	remainingResultTypes := []reflect.Type{}
	for _, result := range results {
		if result.Type() == errorType {
			if !result.IsNil() {
				if err == nil {
					err = juicemud.WithStack(result.Interface().(error))
				} else {
					err = stderrors.Join(juicemud.WithStack(result.Interface().(error)), err)
				}
			}
		} else {
			remainingResults = append(remainingResults, result)
			remainingResultTypes = append(remainingResultTypes, result.Type())
		}
	}
	if len(resultTypes) != len(remainingResults) {
		return nil, errors.Errorf("%v didn't return %v results (excluding errors), it returned %+v", fun, len(resultTypes), remainingResultTypes)
	}
	for index, resultType := range resultTypes {
		if remainingResults[index].Type() != resultType {
			return nil, errors.Errorf("%v returns %v instead of %v as result %v (excluding errors)", fun, remainingResults[index].Type(), resultType, index)
		}
	}
	return remainingResults, err
}

func call(val reflect.Value, methName string, resultTypes []reflect.Type, anyArgs ...any) ([]reflect.Value, error) {
	meth := val.MethodByName(methName)
	if meth.IsZero() {
		return nil, errors.Errorf("%v doesn't have a method %q", val.Type(), methName)
	}
	res, err := callFunc(meth, resultTypes, anyArgs...)
	if err != nil {
		return nil, errors.Wrapf(err, "trying to call %v.%v(%+v) -> %+v", val.Type(), methName, anyArgs, resultTypes)
	}
	return res, nil
}

func callLen(val reflect.Value) (int, error) {
	res, err := call(val, "Len", []reflect.Type{intType})
	if err != nil {
		return 0, errors.Wrapf(err, "trying to call %v.Len() -> [int]", val.Type())
	}
	return int(res[0].Int()), nil
}

func copyList(dst reflect.Value, src *v8go.Value) error {
	srcObj, err := src.AsObject()
	if err != nil {
		return juicemud.WithStack(err)
	}
	if !srcObj.IsArray() {
		return errors.Errorf("%v is no Array", dst.Type())
	}
	dstLen, err := callLen(dst)
	if err != nil {
		return juicemud.WithStack(err)
	}
	srcLenVal, err := srcObj.Get("length")
	if err != nil {
		return juicemud.WithStack(err)
	}
	srcLen := int(srcLenVal.Int32())
	for index := 0; index < min(dstLen, srcLen); index++ {
		atMeth := dst.MethodByName("At")
		if !atMeth.IsValid() {
			return errors.Errorf("no At method in %v", dst.Type())
		}
		atMethType := atMeth.Type()
		if atMethType.NumOut() < 1 {
			return errors.Errorf("%v.At doesn't return anything", dst.Type())
		}
		dstElemType := atMethType.Out(0)
		switch dstTypeOf(dstElemType) {
		case dstUnknown:
			return errors.Errorf("can't copy to a %v", dstElemType)
		case dstPrim:
			srcAt, err := srcObj.Get(fmt.Sprint(index))
			if err != nil {
				return errors.Errorf("can't index %v at %v", srcObj, index)
			}
			srcAtPrim, err := castPrim(srcAt, dstElemType)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if _, err := call(dst, "Set", nil, index, srcAtPrim); err != nil {
				return errors.Wrapf(err, "trying to call %v.Set(%v, %v)", dst.Type(), index, srcAtPrim)
			}
		case dstList:
			return errors.Errorf("can't copy to nested list %v", dstElemType)
		case dstStruct:
			dstAt, err := call(dst, "At", []reflect.Type{dstElemType}, index)
			if err != nil {
				return errors.Wrapf(err, "trying to call %v.At(%v) -> %v", dst.Type(), index, dstElemType)
			}
			srcAt, err := srcObj.Get(fmt.Sprint(index))
			if err != nil {
				return errors.Errorf("can't index %v at %v", srcObj, index)
			}
			if err := copyStruct(dstAt[0], srcAt); err != nil {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

func copyStruct(dst reflect.Value, src *v8go.Value) error {
	srcObj, err := src.AsObject()
	if err != nil {
		return juicemud.WithStack(err)
	}
	dstTyp := dst.Type()
	for methIndex := 0; methIndex < dstTyp.NumMethod(); methIndex++ {
		methName := dstTyp.Method(methIndex).Name
		if strings.HasPrefix(methName, "Set") {
			fieldName := methName[len("Set"):]
			if srcObj.Has(fieldName) {
				srcField, err := srcObj.Get(fieldName)
				if err != nil {
					return juicemud.WithStack(err)
				}
				meth := dst.MethodByName(methName)
				if meth.IsZero() {
					return errors.Errorf("no %v method in a %v??", methName, dstTyp)
				}
				methTyp := meth.Type()
				dstArgType := methTyp.In(0)
				switch dstTypeOf(dstArgType) {
				case dstUnknown:
					return errors.Errorf("can't copy to a %v", dstArgType)
				case dstPrim:
					srcFieldPrim, err := castPrim(srcField, dstArgType)
					if err != nil {
						return juicemud.WithStack(err)
					}
					if _, err := call(dst, methName, nil, srcFieldPrim); err != nil {
						return errors.Wrapf(err, "trying to call %v.%v(%v)", dst.Type(), methName, srcFieldPrim)
					}
				case dstList:
					newList, err := newList(dst, fieldName, dstArgType, srcField)
					if err != nil {
						return juicemud.WithStack(err)
					}
					if err := copyList(newList, srcField); err != nil {
						return juicemud.WithStack(err)
					}
				case dstStruct:
					return errors.Errorf("can't copy to nested struct %v", dstTyp)
				}
			}
		}
	}
	return nil
}

func newList(val reflect.Value, listName string, listType reflect.Type, lengthProvider *v8go.Value) (reflect.Value, error) {
	obj, err := lengthProvider.AsObject()
	if err != nil {
		return reflect.Zero(intType), nil
	}
	if !obj.IsArray() {
		return reflect.Zero(intType), errors.Errorf("%v is no Array", obj)
	}
	lenVal, err := obj.Get("length")
	if err != nil {
		return reflect.Zero(intType), juicemud.WithStack(err)
	}
	res, err := call(val, fmt.Sprintf("New%s", listName), []reflect.Type{listType}, int32(lenVal.Int32()))
	if err != nil {
		return reflect.Zero(intType), errors.Wrapf(err, "trying to call %v.New%s(%v) -> %v", val.Type(), listName, lenVal.Int32(), listType)
	}
	return res[0], nil
}

func CreateAndCopy(createFunc any, src *v8go.Value) error {
	createFuncVal := reflect.ValueOf(createFunc)
	if createFuncVal.Kind() != reflect.Func {
		return errors.Errorf("%v is no create function", createFunc)
	}
	typ := createFuncVal.Type()
	if typ.NumOut() < 1 {
		return errors.Errorf("%v is no create function, it doesn't return anything", createFunc)
	}
	srcObj, err := src.AsObject()
	if err != nil {
		return errors.Errorf("%v is no Object", src)
	}
	if !srcObj.IsArray() {
		return errors.Errorf("%v is no Array", src)
	}
	lenVal, err := srcObj.Get("length")
	if err != nil {
		return juicemud.WithStack(err)
	}
	created, err := callFunc(createFuncVal, []reflect.Type{typ.Out(0)}, lenVal.Int32())
	if err != nil {
		return errors.Wrapf(err, "trying to call %v(%v) -> %v", createFuncVal, lenVal.Int32(), typ.Out(0))
	}
	return copyList(created[0], src)
}

func Copy(dst any, src *v8go.Value) error {
	dstVal := reflect.ValueOf(dst)
	switch dstTypeOf(dstVal.Type()) {
	case dstUnknown:
		fallthrough
	case dstPrim:
		return errors.Errorf("can't copy to a %v", dstVal.Type())
	case dstList:
		return copyList(dstVal, src)
	case dstStruct:
		return copyStruct(dstVal, src)
	}
	return errors.Errorf("unrecognized dstType %v", dstVal.Type())
}

type toPtrer interface {
	ToPtr() (capnp.Ptr, error)
}

func equal(a, b any) (bool, error) {
	var aPtr *capnp.Ptr
	var aBytes []byte
	switch v := a.(type) {
	case toPtrer:
		p, err := v.ToPtr()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		aPtr = &p
	case string:
		aBytes = []byte(v)
	case []byte:
		aBytes = v
	}
	var bPtr *capnp.Ptr
	var bBytes []byte
	switch v := b.(type) {
	case toPtrer:
		p, err := v.ToPtr()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		bPtr = &p
	case string:
		bBytes = []byte(v)
	case []byte:
		bBytes = v
	}
	if aPtr != nil && bPtr != nil {
		return capnp.Equal(*aPtr, *bPtr)
	}
	if aBytes != nil && bBytes != nil {
		return bytes.Equal(aBytes, bBytes), nil
	}
	return false, errors.Errorf("can't compare %v to %v", a, b)
}

type ListType[T any] interface {
	Len() int
	At(int) (T, error)
	Set(int, T) error
}

type ListHelper[V any, T ListType[V]] struct {
	Get func() (T, error)
	New func(int32) (T, error)
}

func (l ListHelper[V, T]) Iter() iter.Seq2[*V, error] {
	return func(yield func(*V, error) bool) {
		list, err := l.Get()
		if err != nil {
			yield(nil, juicemud.WithStack(err))
		} else {
			for i := 0; i < list.Len(); i++ {
				e, err := list.At(i)
				if err != nil {
					yield(nil, juicemud.WithStack(err))
				} else {
					yield(&e, nil)
				}
			}
		}
	}
}

func (l ListHelper[V, T]) All() ([]V, error) {
	list, err := l.Get()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	result := make([]V, list.Len())
	for i := 0; i < list.Len(); i++ {
		el, err := list.At(i)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		result[i] = el
	}
	return result, nil
}

func (l ListHelper[V, T]) Append(v V) error {
	oldList, err := l.Get()
	if err != nil {
		return juicemud.WithStack(err)
	}
	newList, err := l.New(int32(oldList.Len() + 1))
	if err != nil {
		return juicemud.WithStack(err)
	}
	for i := 0; i < oldList.Len(); i++ {
		oldVal, err := oldList.At(i)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := newList.Set(i, oldVal); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return juicemud.WithStack(newList.Set(oldList.Len(), v))
}

func (l ListHelper[V, T]) Has(needle V) (bool, error) {
	for v, err := range l.Iter() {
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		if eq, err := equal(*v, needle); err != nil {
			return false, juicemud.WithStack(err)
		} else if eq {
			return true, nil
		}
	}
	return false, nil
}

func (l ListHelper[V, T]) Set(a []V) error {
	newList, err := l.New(int32(len(a)))
	if err != nil {
		return juicemud.WithStack(err)
	}
	for index, v := range a {
		if err := newList.Set(index, v); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

func (l ListHelper[V, T]) Remove(v V) error {
	oldList, err := l.Get()
	if err != nil {
		return juicemud.WithStack(err)
	}
	foundAt := -1
	for i := 0; i < oldList.Len(); i++ {
		oldVal, err := oldList.At(i)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if eq, err := equal(v, oldVal); err != nil {
			return juicemud.WithStack(err)
		} else if eq {
			foundAt = i
			break
		}
	}
	if foundAt == -1 {
		return nil
	}
	newList, err := l.New(int32(oldList.Len() - 1))
	if err != nil {
		return juicemud.WithStack(err)
	}
	newListIndex := 0
	for i := 0; i < oldList.Len(); i++ {
		if i != foundAt {
			oldVal, err := oldList.At(i)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if err := newList.Set(newListIndex, oldVal); err != nil {
				return juicemud.WithStack(err)
			}
			newListIndex++
		}
	}
	return nil
}
