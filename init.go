package donkeytownsfolk

import (
	"github.com/gorilla/mux"
)

func Init(r *mux.Router) error {
	db, err := OpenDb()
	if err != nil {
		return err
	}

	go db.ScrapeForever()
	SetupRenderer(db, r)
	return nil
}
