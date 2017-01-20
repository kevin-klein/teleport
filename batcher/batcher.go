package batcher

import (
	"fmt"
	"sort"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/jmoiron/sqlx"
	"github.com/pagarme/teleport/action"
	"github.com/pagarme/teleport/client"
	"github.com/pagarme/teleport/database"
)

type Batcher struct {
	db                *database.Database
	targets           map[string]*client.Client
	maxEventsPerBatch int
}

func New(db *database.Database, targets map[string]*client.Client, maxEventsPerBatch int) *Batcher {
	return &Batcher{
		db:                db,
		targets:           targets,
		maxEventsPerBatch: maxEventsPerBatch,
	}
}

// Every sleepTime interval, create a batch with unbatched events
func (b *Batcher) Watch(sleepTime time.Duration) {
	for {
		err := b.createBatches()

		if err != nil {
			log.Errorf("Error creating batch! %v", err)
		}

		time.Sleep(sleepTime)
	}
}

// Group all events 'waiting_batch' and create a batch with them.
func (b *Batcher) createBatches() error {
	// Get events waiting replication
	events, err := b.db.GetEvents("waiting_batch", b.maxEventsPerBatch)

	if err != nil {
		return err
	}

	err = b.db.RefreshSchema()

	if err != nil {
		return err
	}

	// Stop if there are no events
	if len(events) == 0 {
		return nil
	}

	// Start a transaction
	tx := b.db.NewTransaction()

	// Get actions for each target
	actionsForTarget, err := b.actionsForTargets(events)

	if err != nil {
		return err
	}

	// Create batches for each target with the given targets/actions
	_, err = b.CreateBatchesWithActions(actionsForTarget, tx)

	if err != nil {
		return err
	}

	err = b.markEventsBatched(events, tx)

	if err != nil {
		return err
	}

	return tx.Commit()
}

func (b *Batcher) markEventsBatched(events []*database.Event, tx *sqlx.Tx) error {
	for _, event := range events {
		event.Status = "batched"
		err := event.UpdateQuery(tx)

		if err != nil {
			return err
		}
	}

	return nil
}

func (b *Batcher) CreateBatchesWithActions(actionsForTarget map[string][]action.Action, tx *sqlx.Tx) ([]*database.Batch, error) {
	batches := make([]*database.Batch, 0)

	// Create a batch for each target
	// for targetName, target := range b.targets {
	for targetName, actions := range actionsForTarget {
		currentBatch := make([]action.Action, 0)

		// Create batch with pending items to batch
		createBatch := func() error {
			batch, err := b.createBatchWithActions(currentBatch, targetName, tx)

			if err != nil {
				return err
			}

			if batch != nil {
				batches = append(batches, batch)
			}

			return nil
		}

		for _, act := range actions {
			if act.NeedsSeparatedBatch() {
				// Create batch with pending items to batch
				if err := createBatch(); err != nil {
					return nil, err
				}

				// Then create the new batch containing only this event
				currentBatch = []action.Action{act}
				if err := createBatch(); err != nil {
					return nil, err
				}

				// Reset currentBatch for next events
				currentBatch = make([]action.Action, 0)
			} else {
				// If action doesn't need separate batch, simply append the batch
				currentBatch = append(currentBatch, act)
			}
		}

		if err := createBatch(); err != nil {
			return nil, err
		}
	}

	return batches, nil
}

func (b *Batcher) createBatchWithActions(actions []action.Action, targetName string, tx *sqlx.Tx) (*database.Batch, error) {
	// Don't create batch if there are no events
	if len(actions) == 0 {
		return nil, nil
	}

	// Allocate a new batch
	batch := database.NewBatch("db")

	// Set actions
	batch.SetActions(actions)

	batch.DataStatus = "transmitted"

	// Set source and target
	batch.Source = b.db.Name
	batch.Target = targetName

	// Insert batch
	err := batch.InsertQuery(tx)

	if err != nil {
		return nil, err
	}

	log.Infof("Generated new batch: %#v", batch)

	return batch, nil
}

func (b *Batcher) actionsForTargets(events database.Events) (map[string][]action.Action, error) {
	// Sort events by id first
	sort.Sort(events)

	actionsForTarget := make(map[string][]action.Action)

	for targetName, target := range b.targets {
		schema, columnExpression := database.ParseTargetExpression(target.TargetExpression)
		actions := make([]action.Action, 0)

		// Filter actions for the current target
		filterActions := func(actions []action.Action) []action.Action {
			filtered := make([]action.Action, 0)

			for _, act := range actions {
				if act.Filter(fmt.Sprintf("%s.%s", target.ApplySchema, columnExpression)) {
					filtered = append(filtered, act)
				}
			}

			return filtered
		}

		// Build actions for each event
		for _, event := range events {
			if event.Kind == "ddl" {
				ddl := database.NewDdl(b.db, []byte(*event.Data), schema, target.ApplySchema)

				// Update database schema
				for _, schema := range ddl.PostSchemas {
					b.db.Schemas[schema.Name] = schema
				}

				actions = append(actions, filterActions(ddl.Diff())...)
			} else if event.Kind == "dml" {
				dml := database.NewDml(b.db, event, []byte(*event.Data), target.ApplySchema)
				actions = append(actions, filterActions(dml.Diff())...)
			}
		}

		actionsForTarget[targetName] = actions
	}

	return actionsForTarget, nil
}
