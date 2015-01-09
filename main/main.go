package main

import (
	"fmt"
	"github.com/garbotron/donkeytownsfolk"
	"github.com/gorilla/mux"
	"net/http"
)

const httpPort = 8080

func main() {
	db, err := donkeytownsfolk.OpenDb()
	if err != nil {
		panic(err)
	}

	r := mux.NewRouter()
	donkeytownsfolk.SetupRenderer(db, r)
	http.Handle("/", r)
	http.ListenAndServe(fmt.Sprintf(":%d", httpPort), nil)
}
