package glue

import (
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"log"
	"reflect"
	"strings"

	"capnproto.org/go/capnp/v3"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"rogchap.com/v8go"
)

type glueType int

const (
	glueUnknown glueType = iota
	gluePrim
	glueBytes
	glueStruct
	glueList
)

type undefTypeMarker int

var (
	capnpListType   = reflect.TypeOf(capnp.ListKind{})
	capnpStructType = reflect.TypeOf(capnp.StructKind{})
	errorType       = reflect.TypeOf((*error)(nil)).Elem()
	undefType       = reflect.TypeOf(undefTypeMarker(0))
	intType         = reflect.TypeOf(0)
	bytesType       = reflect.TypeOf([]byte{})
)

type glueData struct {
	glueType glueType
	fromV8   func(*v8go.Value, reflect.Type) (any, error)
	toV8     func(*v8go.Context, reflect.Value) (*v8go.Value, error)
}

func canNotFromV8(val *v8go.Value, dstType reflect.Type) (any, error) {
	return nil, errors.Errorf("%v can't be cast to %v", val, dstType)
}

func canNotToV8(vctx *v8go.Context, val reflect.Value) (*v8go.Value, error) {
	return nil, errors.Errorf("%v can't be cast to *v8go.Value", val.Type())
}

func getGlueData(typ reflect.Type) glueData {
	switch typ.Kind() {
	case reflect.Int:
		fallthrough
	case reflect.Int8:
		fallthrough
	case reflect.Int16:
		fallthrough
	case reflect.Int32:
		fallthrough
	case reflect.Uint:
		fallthrough
	case reflect.Uint8:
		fallthrough
	case reflect.Uint16:
		fallthrough
	case reflect.Uint32:
		return glueData{
			glueType: gluePrim,
			fromV8: func(val *v8go.Value, dstType reflect.Type) (any, error) {
				return reflect.ValueOf(val.Int32()).Convert(dstType).Interface(), nil
			},
			toV8: func(vctx *v8go.Context, val reflect.Value) (*v8go.Value, error) {
				return v8go.NewValue(vctx.Isolate(), int32(val.Int()))
			},
		}
	case reflect.Int64:
		fallthrough
	case reflect.Uint64:
		return glueData{
			glueType: gluePrim,
			fromV8: func(val *v8go.Value, dstType reflect.Type) (any, error) {
				return reflect.ValueOf(val.Integer()).Convert(dstType).Interface(), nil
			},
			toV8: func(vctx *v8go.Context, val reflect.Value) (*v8go.Value, error) {
				return v8go.NewValue(vctx.Isolate(), val.Int())
			},
		}
	case reflect.Float32:
		fallthrough
	case reflect.Float64:
		return glueData{
			glueType: gluePrim,
			fromV8: func(val *v8go.Value, dstType reflect.Type) (any, error) {
				return reflect.ValueOf(val.Number()).Convert(dstType).Interface(), nil
			},
			toV8: func(vctx *v8go.Context, val reflect.Value) (*v8go.Value, error) {
				return vctx.RunScript(fmt.Sprint(val.Interface()), "getGlueData")
			},
		}
	case reflect.String:
		return glueData{
			glueType: gluePrim,
			fromV8: func(val *v8go.Value, dstType reflect.Type) (any, error) {
				return reflect.ValueOf(val.String()).Convert(dstType).Interface(), nil
			},
			toV8: func(vctx *v8go.Context, val reflect.Value) (*v8go.Value, error) {
				return v8go.NewValue(vctx.Isolate(), val.String())
			},
		}
	case reflect.Slice:
		if typ == bytesType {
			return glueData{
				glueType: glueBytes,
				fromV8: func(val *v8go.Value, dstType reflect.Type) (any, error) {
					b, err := hex.DecodeString(val.String())
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					return reflect.ValueOf(b).Convert(dstType).Interface(), nil
				},
				toV8: func(vctx *v8go.Context, val reflect.Value) (*v8go.Value, error) {
					return v8go.NewValue(vctx.Isolate(), hex.EncodeToString(val.Interface().([]byte)))
				},
			}
		}
	case reflect.Struct:
		switch {
		case typ.ConvertibleTo(capnpListType):
			return glueData{
				glueType: glueList,
				fromV8:   canNotFromV8,
				toV8:     canNotToV8,
			}
		case typ.ConvertibleTo(capnpStructType):
			return glueData{
				glueType: glueStruct,
				fromV8:   canNotFromV8,
				toV8:     canNotToV8,
			}
		}
	}
	return glueData{
		glueType: glueUnknown,
		fromV8:   canNotFromV8,
		toV8:     canNotToV8,
	}
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
		if resultType != undefType && remainingResults[index].Type() != resultType {
			return nil, errors.Errorf("%v returns %v instead of %v as result %v (excluding errors)", fun, remainingResults[index].Type(), resultType, index)
		}
	}
	return remainingResults, err
}

func callMeth(val reflect.Value, methName string, resultTypes []reflect.Type, anyArgs ...any) ([]reflect.Value, error) {
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
	res, err := callMeth(val, "Len", []reflect.Type{intType})
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
		data := getGlueData(dstElemType)
		switch data.glueType {
		case glueUnknown:
			return errors.Errorf("can't copy to a %v", dstElemType)
		case glueBytes:
			fallthrough
		case gluePrim:
			srcAt, err := srcObj.Get(fmt.Sprint(index))
			if err != nil {
				return errors.Errorf("can't index %v at %v", srcObj, index)
			}
			srcAtPrim, err := data.fromV8(srcAt, dstElemType)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if _, err := callMeth(dst, "Set", nil, index, srcAtPrim); err != nil {
				return errors.Wrapf(err, "trying to call %v.Set(%v, %v)", dst.Type(), index, srcAtPrim)
			}
		case glueList:
			return errors.Errorf("can't copy to nested list %v", dstElemType)
		case glueStruct:
			dstAt, err := callMeth(dst, "At", []reflect.Type{dstElemType}, index)
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
				data := getGlueData(dstArgType)
				switch data.glueType {
				case glueUnknown:
					return errors.Errorf("can't copy to a %v", dstArgType)
				case glueBytes:
					fallthrough
				case gluePrim:
					srcFieldPrim, err := data.fromV8(srcField, dstArgType)
					if err != nil {
						return juicemud.WithStack(err)
					}
					if _, err := callMeth(dst, methName, nil, srcFieldPrim); err != nil {
						return errors.Wrapf(err, "trying to call %v.%v(%v)", dst.Type(), methName, srcFieldPrim)
					}
				case glueList:
					newList, err := newList(dst, fieldName, srcField)
					if err != nil {
						return juicemud.WithStack(err)
					}
					if err := copyList(newList, srcField); err != nil {
						return juicemud.WithStack(err)
					}
				case glueStruct:
					return errors.Errorf("can't copy to nested struct %v", dstTyp)
				}
			}
		}
	}
	return nil
}

func newList(val reflect.Value, listName string, lengthProvider *v8go.Value) (reflect.Value, error) {
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
	res, err := callMeth(val, fmt.Sprintf("New%s", listName), []reflect.Type{undefType}, int32(lenVal.Int32()))
	if err != nil {
		return reflect.Zero(intType), errors.Wrapf(err, "trying to call %v.New%s(%v) -> anything", val.Type(), listName, lenVal.Int32())
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
	return juicemud.WithStack(copyList(created[0], src))
}

func Copy(dst any, src *v8go.Value) error {
	dstVal := reflect.ValueOf(dst)
	data := getGlueData(dstVal.Type())
	switch data.glueType {
	case glueUnknown:
		fallthrough
	case glueBytes:
		fallthrough
	case gluePrim:
		return errors.Errorf("can't copy to a %v", dstVal.Type())
	case glueList:
		return copyList(dstVal, src)
	case glueStruct:
		return copyStruct(dstVal, src)
	}
	return errors.Errorf("unrecognized dstType %v", dstVal.Type())
}

func structToV8(vctx *v8go.Context, src reflect.Value) (*v8go.Value, error) {
	dst, err := v8go.NewObjectTemplate(vctx.Isolate()).NewInstance(vctx)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	srcTyp := src.Type()
	for index := 0; index < srcTyp.NumMethod(); index++ {
		methName := srcTyp.Method(index).Name
		log.Printf("meth name is %q", methName)
		if strings.HasPrefix(methName, "Set") {
			fieldName := methName[len("Set"):]
			srcVal, err := callMeth(src, fieldName, []reflect.Type{undefType})
			if err != nil {
				return nil, juicemud.WithStack(err)
			}
			v8Val, err := toV8(vctx, srcVal[0])
			if err != nil {
				return nil, juicemud.WithStack(err)
			}
			if err := dst.Set(fieldName, v8Val); err != nil {
				return nil, juicemud.WithStack(err)
			}
		}
	}
	return dst.Value, nil
}

func listToV8(vctx *v8go.Context, src reflect.Value) (*v8go.Value, error) {
	dst, err := vctx.RunScript("new Array()", "listToV8")
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	dstObj, err := dst.AsObject()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	length, err := callLen(src)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	for index := 0; index < length; index++ {
		atVals, err := callMeth(src, "At", []reflect.Type{undefType}, index)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		v8Val, err := toV8(vctx, atVals[0])
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		if _, err = dstObj.MethodCall("push", v8Val); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	return dst, nil
}

func toV8(vctx *v8go.Context, src reflect.Value) (*v8go.Value, error) {
	data := getGlueData(src.Type())
	switch data.glueType {
	case glueBytes:
		fallthrough
	case gluePrim:
		return data.toV8(vctx, src)
	case glueList:
		return listToV8(vctx, src)
	case glueStruct:
		return structToV8(vctx, src)
	}
	return nil, errors.Errorf("can't convert %v to *v8go.Value", src)
}

func ToV8(vctx *v8go.Context, src any) (*v8go.Value, error) {
	return toV8(vctx, reflect.ValueOf(src))
}
