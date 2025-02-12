package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"

	goccy "github.com/goccy/go-json"
)

type data struct {
	Objects []structs.Object
}

func main() {
	dir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings.")
	dataPath := flag.String("data", "", "Path to load JSON from.")

	flag.Parse()

	if *dataPath == "" {
		flag.Usage()
		return
	}

	ctx := context.Background()
	store, err := storage.New(ctx, *dir)
	if err != nil {
		log.Fatal(err)
	}
	_, err = game.New(ctx, store)
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Open(*dataPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	data := &data{}
	if err := goccy.NewDecoder(f).Decode(data); err != nil {
		log.Fatal(err)
	}

	realIDs := map[string]string{}
	for _, obj := range data.Objects {
		if realIDs[obj.Id], err = structs.NextObjectID(); err != nil {
			log.Fatal(err)
		}
	}

	replace := func(id *string) {
		oldID := *id
		var found bool
		if *id, found = realIDs[oldID]; !found {
			log.Fatal("old ID %q not found among real IDs", oldID)
		}
	}

	for i := range data.Objects {
		obj := &data.Objects[i]
		replace(&obj.Id)
		replace(&obj.Location)
		for j := range obj.Exits {
			exit := &obj.Exits[j]
			replace(&exit.Destination)
		}
		obj.SourcePath = fmt.Sprintf("/%s.js", obj.Id)
		sourceBuf := &bytes.Buffer{}
		descBytes, err := goccy.MarshalIndent(obj.Descriptions, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		exitBytes, err := goccy.MarshalIndent(obj.Exits, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(sourceBuf, `// Source for %v/%v

setDescriptions(%s);

setExits(%s);
`, obj.Name(), obj.Id, string(descBytes), string(exitBytes))
		if err := store.UNSAFEEnsureObject(ctx, obj); err != nil {
			log.Fatal(err)
		}
		if err := store.StoreSource(ctx, obj.SourcePath, sourceBuf.Bytes()); err != nil {
			log.Fatal(err)
		}
	}
}
