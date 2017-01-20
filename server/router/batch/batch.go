package batch

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/mux"
	"github.com/pagarme/teleport/database"
	"github.com/pagarme/teleport/server/httputils"
	"github.com/pagarme/teleport/server/router"
)

type batchRouter struct {
	routes []router.Route
	db     *database.Database
}

func New(db *database.Database) *batchRouter {
	b := &batchRouter{}
	b.db = db
	b.initRoutes()
	return b
}

func (b *batchRouter) create(w http.ResponseWriter, r *http.Request) error {
	// Parse batch data from request
	var newBatch database.Batch
	json.NewDecoder(r.Body).Decode(&newBatch)

	// Start transaction
	tx := b.db.NewTransaction()

	// Batches with db storage are ready to be applied
	newBatch.Status = "waiting_apply"

	if newBatch.StorageType == "fs" {
		// Other batches need to wait for data from source
		newBatch.DataStatus = "waiting_data"
		newData := fmt.Sprintf("out_%s", *newBatch.Data)
		newBatch.Data = &newData
	}

	// Insert
	err := newBatch.InsertQuery(tx)

	if err != nil {
		return err
	}

	// Commit transaction
	err = tx.Commit()

	if err != nil {
		return err
	}

	log.Infof("Received batch: %v", newBatch)

	// Respond HTTP OK
	return httputils.WriteJSON(w, http.StatusOK, nil)
}

func (b *batchRouter) update(w http.ResponseWriter, r *http.Request) error {
	vars := mux.Vars(r)
	batchId := vars["id"]

	batch, err := b.db.GetBatch(batchId)

	if err != nil {
		return err
	}

	if batch == nil {
		return fmt.Errorf("batch not found!")
	}

	if batch.DataStatus != "waiting_data" {
		return fmt.Errorf("batch is not waiting for data!")
	}

	if err != nil {
		return err
	}

	log.Infof("Receiving data for batch: %v", batch)

	out, err := batch.CreateFile()

	if err != nil {
		return fmt.Errorf("unable to create the file for writing!")
	}

	defer out.Close()

	// write the content from POST to the file
	_, err = io.Copy(out, r.Body)

	if err != nil {
		return err
	}

	// Start transaction
	tx := b.db.NewTransaction()

	batch.DataStatus = "transmitted"
	err = batch.UpdateQuery(tx)

	if err != nil {
		return err
	}

	// Commit transaction
	err = tx.Commit()

	if err != nil {
		return err
	}

	log.Infof("Received data for batch: %v", batch)

	// Respond HTTP OK
	return httputils.WriteJSON(w, http.StatusOK, nil)
}

func (b *batchRouter) Routes() []router.Route {
	return b.routes
}

func (b *batchRouter) initRoutes() {
	b.routes = []router.Route{
		router.NewPostRoute("/batches", b.create),
		router.NewPostRoute("/batches/{id}", b.update),
	}
}
