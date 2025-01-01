package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zond/juicemud/digest"
)

func main() {
	username := flag.String("username", "", "username to compute HA1 for")
	password := flag.String("password", "", "password to compute HA1 for")
	realm := flag.String("realm", "WebDAV", "realm to compute HA1 for")

	flag.Parse()

	if *username == "" || *password == "" {
		flag.Usage()
		os.Exit(1)
	}

	fmt.Println(digest.ComputeHA1(*username, *realm, *password))
}
