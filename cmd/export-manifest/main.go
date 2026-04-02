package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/kumiho-plugin/kumiho-plugin-metadata-kitsu/plugin"
)

func main() {
	manifest := plugin.New("", "", "", nil).Manifest()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		log.Fatal(err)
	}
}
