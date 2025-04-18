package main

import (
	"flag"
	"fmt"
	"go/types"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/dave/jennifer/jen"
	"golang.org/x/tools/go/packages"
)

var (
	doRegexp  = regexp.MustCompile(`^(.*)DO`)
	claRegexp = regexp.MustCompile(`command-line-arguments\.`)
)

func cap(s string) string {
	return strings.ToUpper(s[0:1]) + s[1:]
}

func main() {
	in := flag.String("in", "", "file to read")
	out := flag.String("out", "", "file to write")
	pkg := flag.String("pkg", "", "package of out")

	flag.Parse()

	if *in == "" || *out == "" || *pkg == "" {
		flag.Usage()
		os.Exit(1)
	}

	cfg := &packages.Config{Mode: packages.NeedTypes}
	pkgs, err := packages.Load(cfg, *in)
	if err != nil {
		log.Panic(err)
	}

	f := jen.NewFile(*pkg)
	f.PackageComment("Code generated by generator, DO NOT EDIT.")

	for _, pkg := range pkgs {
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if match := doRegexp.FindStringSubmatch(obj.Name()); match != nil {
				if structType, ok := obj.Type().Underlying().(*types.Struct); ok {
					backendName := cap(strings.ToLower(match[1]))
					postUnlockTypeName := fmt.Sprintf("PostUnlock%s", backendName)
					f.Type().Id(postUnlockTypeName).Func().Params(jen.Op("*").Id(backendName))
					f.Func().Params(
						jen.Id("f").Id(postUnlockTypeName),
					).Id("call").Params(
						jen.Id("v").Op("*").Id(backendName),
					).Block(
						jen.If(jen.Id("f").Op("!=").Id("nil")).Block(
							jen.Id("f").Call(jen.Id("v")),
						),
					)
					fields := []jen.Code{}
					fields = append(fields, jen.Id("Unsafe").Op("*").Id(match[0]))
					fields = append(fields, jen.Id("PostUnlock").Id(postUnlockTypeName).Tag(map[string]string{"json": "-", "faker": "-"}))
					fields = append(fields, jen.Id("mutex").Qual("sync", "RWMutex"))
					f.Type().Id(backendName).Struct(fields...)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("UnsafeShallowCopy").Params().Op("*").Id(backendName).Block(
						jen.Return(
							jen.Op("&").Id(backendName).Block(
								jen.Id("Unsafe").Op(":").Id("v").Dot("Unsafe").Op(","),
								jen.Id("PostUnlock").Op(":").Id("v").Dot("PostUnlock").Op(","),
							),
						),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("Describe").Params().Id("string").Block(
						jen.Id("b").Op(",").Id("_").Op(":=").Qual("github.com/goccy/go-json", "MarshalIndent").Call(
							jen.Id("v").Dot("Unsafe"),
							jen.Lit(""),
							jen.Lit("  "),
						),
						jen.Return(jen.Id("string").Call(jen.Id("b"))),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("Lock").Params().Block(
						jen.Id("v").Dot("mutex").Dot("Lock").Call(),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("Unlock").Params().Block(
						jen.Id("v").Dot("mutex").Dot("Unlock").Call(),
						jen.Id("v").Dot("PostUnlock").Dot("call").Call(
							jen.Id("v"),
						),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("RLock").Params().Block(
						jen.Id("v").Dot("mutex").Dot("RLock").Call(),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("RUnlock").Params().Block(
						jen.Id("v").Dot("mutex").Dot("RUnlock").Call(),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("Size").Params().Id("int").Block(
						jen.Return(jen.Id("v").Dot("Unsafe").Dot("Size").Call()),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("Marshal").Params(
						jen.Id("b").Id("[]byte"),
					).Block(
						jen.Id("v").Dot("RLock").Call(),
						jen.Defer().Id("v").Dot("RUnlock").Call(),
						jen.Id("v").Dot("Unsafe").Dot("Marshal").Call(jen.Id("b")),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("Unmarshal").Params(
						jen.Id("b").Id("[]byte"),
					).Id("error").Block(
						jen.Id("v").Dot("Lock").Call(),
						jen.Defer().Id("v").Dot("Unlock").Call(),
						jen.Id("v").Dot("Unsafe").Op("=").Id("new").Call(jen.Id(match[0])),
						jen.Return(jen.Id("v").Dot("Unsafe").Dot("Unmarshal").Call(jen.Id("b"))),
					)
					f.Func().Params(
						jen.Id("v").Op("*").Id(backendName),
					).Id("SetPostUnlock").Params(jen.Id("p").Func().Params(jen.Op("*").Id(backendName))).Block(
						jen.Id("v").Dot("PostUnlock").Op("=").Id("p"),
					)
					for i := 0; i < structType.NumFields(); i++ {
						field := structType.Field(i)
						f.Func().Params(
							jen.Id("v").Op("*").Id(backendName),
						).Id(fmt.Sprintf("Get%s", field.Name())).Params().Id(claRegexp.ReplaceAllString(field.Type().String(), "")).Block(
							jen.Id("v").Dot("RLock").Call(),
							jen.Defer().Id("v").Dot("RUnlock").Call(),
							jen.Return(jen.Id("v").Dot("Unsafe").Dot(field.Name())),
						)
						f.Func().Params(
							jen.Id("v").Op("*").Id(backendName),
						).Id(fmt.Sprintf("Set%s", field.Name())).Params(
							jen.Id("p").Id(claRegexp.ReplaceAllString(field.Type().String(), "")),
						).Block(
							jen.Id("v").Dot("Lock").Call(),
							jen.Defer().Id("v").Dot("Unlock").Call(),
							jen.Id("v").Dot("Unsafe").Dot(field.Name()).Op("=").Id("p"),
						)
					}
				}
			}
		}
	}

	if err := f.Save(*out); err != nil {
		log.Panic(err)
	}
}
