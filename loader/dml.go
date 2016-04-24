package loader

import (
	"fmt"
	"log"
	"github.com/jmoiron/sqlx"
	"github.com/pagarme/teleport/action"
	"github.com/pagarme/teleport/database"
	"strings"
	"sort"
)

func (l *Loader) getDMLBatchEvents(events []*database.Event) (map[*database.Event]*database.Batch, error) {
	eventBatches := make(map[*database.Event]*database.Batch)

	for _, event := range events {
		if event.Kind == "dml_batch" {
			// newEvents = append(newEvents, event)
			batch, err := l.db.GetBatch(*event.Data)

			if err != nil {
				return eventBatches, err
			}

			eventBatches[event] = batch
		}
	}

	return eventBatches, nil
}

func (l *Loader) createDMLEvents() (map[*database.Event]*database.Batch, error) {
	tx := l.db.NewTransaction()
	eventBatches := make(map[*database.Event]*database.Batch)

	for _, schema := range l.db.Schemas {
		for _, class := range schema.Classes {
			if !action.IsInTargetExpression(&l.target.TargetExpression, &schema.Name, &class.RelationName) {
				continue
			}

			// r = regular table
			if class.RelationKind != "r" {
				continue
			}

			if class.GetPrimaryKey() == nil {
				continue
			}

			// Create a new batch with initial data
			batch := database.NewBatch("fs")
			batch.DataStatus = "waiting_data"
			batch.Source = l.db.Name
			batch.Target = l.targetName
			initialData := ""
			batch.SetData(&initialData)

			err := batch.InsertQuery(tx)

			if err != nil {
				return eventBatches, err
			}

			event := &database.Event{
				Kind:          "dml_batch",
				Status:        "building",
				TriggerTag:    fmt.Sprintf("%s.%s", schema.Name, class.RelationName),
				TriggerEvent:  "dml_initial_load",
				TransactionId: "0",
				Data:          &batch.Id,
			}

			err = event.InsertQuery(tx)

			if err != nil {
				return eventBatches, err
			}

			eventBatches[event] = batch
		}
	}

	err := tx.Commit()

	if err != nil {
		return eventBatches, err
	}

	return eventBatches, nil
}

func (l *Loader) resumeDMLEvents(eventBatches map[*database.Event]*database.Batch) error {
	events := make(database.Events, 0)

	for event, _ := range eventBatches {
		events = append(events, event)
	}

	// Sort events by key
	sort.Sort(events)

	for _, event := range events {
		if event.Kind != "dml_batch" {
			continue
		}

		err := l.resumeDMLEvent(event, eventBatches[event])

		if err != nil {
			return err
		}

		log.Printf("Ended processing event %#v\n", event)
	}

	return nil
}

func (l *Loader) getDMLEventSchemaClass(event *database.Event) (*database.Schema, *database.Class) {
	separator := strings.Split(event.TriggerTag, ".")
	schema := l.db.Schemas[separator[0]]
	var class *database.Class

	for _, c := range schema.Classes {
		if c.RelationName == separator[1] {
			class = c
			break
		}
	}

	return schema, class
}

func (l *Loader) generateColumnsForAttributes(attributes []*database.Attribute) map[string]action.Column {
	attributeCol := make(map[string]action.Column)

	for _, attr := range attributes {
		attributeCol[attr.Name] = action.Column{
			attr.Name,
			attr.TypeName,
			attr.IsNativeType(),
		}
	}

	return attributeCol
}

func (l *Loader) resumeDMLEvent(event *database.Event, batch *database.Batch) error {
	tx := l.db.NewTransaction()

	// // REPEATABLE READ is needed to avoid fetching rows that
	// // would be updated both by the trigger flow AND the initial
	// // load (all rows inserted before the initial load start)
	// _, err := tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ;")

	// if err != nil {
	// 	return err
	// }

	schema, class := l.getDMLEventSchemaClass(event)
	tableCount, err := l.getTableCount(tx, schema, class)

	if err != nil {
		return err
	}

	colsForAttributes := l.generateColumnsForAttributes(class.Attributes)

	event.Status = "batched"
	err = event.UpdateQuery(tx)

	if err != nil {
		return err
	}

	log.Printf("Generated new batch: %#v\n", batch)

	if err != nil {
		return err
	}

	batch.DataStatus = "waiting_transmission"
	err = batch.UpdateQuery(tx)

	if err != nil {
		return err
	}

	// Generate OFFSET/LIMITs to iterate
	for i := 0; i < tableCount; i += l.BatchSize {
		rows, err := l.fetchRows(tx, schema, class, l.BatchSize, i)

		if err != nil {
			return err
		}

		actions := make([]action.Action, 0)

		for _, row := range rows {
			actionRows := make([]action.Row, 0)

			for key, value := range *row {
				actionRows = append(actionRows, action.Row{
					value,
					colsForAttributes[key],
				})
			}

			act := &action.InsertRow{
				l.target.ApplySchema,
				class.RelationName,
				class.GetPrimaryKey().Name,
				actionRows,
			}

			actions = append(actions, act)
		}

		err = batch.AppendActions(actions)

		if err != nil {
			return err
		}
	}

	log.Printf("Updated data of batch: %#v\n", batch)

	return tx.Commit()
}

func (l *Loader) getTableCount(tx *sqlx.Tx, schema *database.Schema, table *database.Class) (int, error) {
	var count int

	err := tx.Get(&count,
		fmt.Sprintf(
			`SELECT count(*) FROM "%s"."%s";`,
			schema.Name,
			table.RelationName,
		),
	)

	return count, err
}

func (l *Loader) fetchRows(tx *sqlx.Tx, schema *database.Schema, table *database.Class, limit, offset int) ([]*map[string]interface{}, error) {
	result := make([]*map[string]interface{}, 0)

	rows, err := tx.Queryx(
		fmt.Sprintf(
			`SELECT * FROM "%s"."%s" ORDER BY "%s" LIMIT %d OFFSET %d;`,
			schema.Name,
			table.RelationName,
			table.GetPrimaryKey().Name,
			limit,
			offset,
		),
	)

	if err != nil {
		return []*map[string]interface{}{}, err
	}

	defer rows.Close()

	for rows.Next() {
		results := make(map[string]interface{})
		err = rows.MapScan(results)

		if err != nil {
			return []*map[string]interface{}{}, err
		}

		result = append(result, &results)
	}

	return result, nil
}
