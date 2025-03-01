package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"

	goccy "github.com/goccy/go-json"
)

type data struct {
	Objects []structs.ObjectDO
	Sources map[string]string
}

var nonWordReg = regexp.MustCompile(`\W`)

func main() {
	dir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings.")
	dataPath := flag.String("data", "", "Path to load JSON from.")
	doRestore := flag.Bool("restore", false, "XOR 'backup': Whether to load data from the data path to the database dir.")
	doBackup := flag.Bool("backup", false, "XOR 'restore': Whether to load data from the database dir to the data path.")

	flag.Parse()

	if *dataPath == "" || (*doRestore == *doBackup) {
		flag.Usage()
		return
	}

	ctx := juicemud.MakeMainContext(context.Background())

	store, err := storage.New(ctx, *dir)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := game.New(ctx, store); err != nil {
		log.Fatal(err)
	}

	defer store.Close()

	if *doRestore {
		f, err := os.Open(*dataPath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()

		d := &data{}
		if err := goccy.NewDecoder(f).Decode(d); err != nil {
			log.Fatalf("decoding data: %v", err)
		}

		for _, obj := range d.Objects {
			if err := store.UNSAFEEnsureObject(ctx, &structs.Object{Unsafe: &obj}); err != nil {
				log.Fatalf("storing obj %q: %v", obj.Id, err)
			}
		}
		for path, src := range d.Sources {
			if _, _, err := store.EnsureFile(ctx, path); err != nil {
				log.Fatalf("creating file %q: %v", path, err)
			}
			if err := store.StoreSource(ctx, path, []byte(src)); err != nil {
				log.Fatalf("storing source %q: %v", path, err)
			}
		}
	}
	if *doBackup {
		d := &data{
			Objects: []structs.ObjectDO{},
			Sources: map[string]string{},
		}
		for entry, err := range store.EachSource(ctx) {
			if err != nil {
				log.Fatalf("iterating sources: %v", err)
			}
			d.Sources[entry.Path] = entry.Content
		}
		for obj, err := range store.EachObject(ctx) {
			if err != nil {
				log.Fatalf("iterating objects: %v", err)
			}
			d.Objects = append(d.Objects, *obj.Unsafe)
		}

		b, err := goccy.MarshalIndent(d, "", "  ")
		if err != nil {
			log.Fatalf("encoding data: %v", err)
		}

		if err := os.WriteFile(*dataPath, b, 0x600); err != nil {
			log.Fatal(err)
		}
	}
}
