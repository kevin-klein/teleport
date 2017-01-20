package applier

import (
	"io"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/pagarme/teleport/action"
	"github.com/pagarme/teleport/database"
)

type Applier struct {
	db        *database.Database
	batchSize int
}

func New(db *database.Database, batchSize int) *Applier {
	return &Applier{
		db:        db,
		batchSize: batchSize,
	}
}

// Transmit batches
func (a *Applier) Watch(sleepTime time.Duration) {
	for {
		batches, err := a.db.GetBatches("waiting_apply", "")

		if err != nil {
			log.Errorf("Error fetching batches to apply! %v", err)
		} else {
			for _, batch := range batches {
				shouldContinue, err := a.applyBatch(batch)

				if err != nil {
					log.Errorf("Error applying batch %s: %v", batch.Id, err)
				}

				if !shouldContinue {
					break
				}
			}
		}

		time.Sleep(sleepTime)
	}
}

func (a *Applier) applyAction(act action.Action, context *action.Context) error {
	// Execute action of the given event
	err := act.Execute(context)

	if err != nil {
		log.Errorf("Error applying action %#v: %v", act, err)
		return err
	}

	return nil
}

// Apply a batch
func (a *Applier) applyBatch(batch *database.Batch) (bool, error) {
	if batch.DataStatus != "transmitted" && batch.DataStatus != "waiting_apply" {
		return false, nil
	}

	// Start transaction
	tx := a.db.NewTransaction()
	context := action.NewContext(tx, a.db.Db)

	flushBatch := func() (bool, error) {
		err := context.FlushStatements()

		if err != nil {
			return false, err
		}

		err = batch.UpdateQuery(tx)

		if err != nil {
			return false, err
		}

		err = context.Commit()

		if err != nil {
			return false, err
		}

		return true, nil
	}

	// Update batch status based on a error
	updateBatchStatus := func(previousErr error) (bool, error) {
		if previousErr != nil {
			// Create new transaction because the old one failed
			tx.Rollback()
			tx = a.db.NewTransaction()
			context = action.NewContext(tx, a.db.Db)

			batch.Status = "waiting_apply"
			batch.DataStatus = ""
			batch.WaitingReexecution = true
		} else {
			// Mark batch as applied (no error)
			batch.Status = "applied"
			batch.DataStatus = "applied"
			batch.WaitingReexecution = false
		}

		_, err := flushBatch()

		if err != nil {
			return false, err
		}

		return true, previousErr
	}

	if batch.StorageType == "db" {
		actions, err := batch.GetActions()

		if err != nil {
			return false, err
		}

		for _, act := range actions {
			err := a.applyAction(act, context)

			if err != nil {
				return updateBatchStatus(err)
			}
		}
	} else if batch.StorageType == "fs" {
		reader, file, err := batch.GetFileReader()

		if err != nil {
			return updateBatchStatus(err)
		}

		defer file.Close()

		var act action.Action
		currentStatement := 0
		currentBatchSize := 0
		previousStatement := batch.LastExecutedStatement

		act, err = batch.ReadAction(reader)

		for err == nil {
			// Increment current statement
			currentStatement += 1

			// Start applying from previous stop point
			if currentStatement > previousStatement {
				err = a.applyAction(act, context)

				if err != nil {
					return updateBatchStatus(err)
				}

				batch.LastExecutedStatement += 1
				currentBatchSize += 1

				if currentBatchSize >= a.batchSize {
					shouldContinue, err := flushBatch()

					if err != nil {
						return shouldContinue, err
					}

					// Restart transaction
					tx = a.db.NewTransaction()

					context = action.NewContext(tx, a.db.Db)

					// Reset batch size
					currentBatchSize = 0
				}
			}

			// Read next action
			act, err = batch.ReadAction(reader)
		}

		if err != io.EOF {
			return updateBatchStatus(err)
		}
	}

	log.Infof("Applied batch: %v", batch)

	return updateBatchStatus(nil)
}
