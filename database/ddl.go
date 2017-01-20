package database

import (
	"encoding/json"

	"github.com/pagarme/teleport/action"
	"github.com/pagarme/teleport/batcher/ddldiff"
)

type Ddl struct {
	PreSchemas   []*Schema `json:"pre"`
	PostSchemas  []*Schema `json:"post"`
	Db           *Database
	SourceSchema string
	TargetSchema string
}

func NewDdl(db *Database, data []byte, sourceSchema, targetSchema string) *Ddl {
	var ddl Ddl
	err := json.Unmarshal(data, &ddl)

	if err != nil {
		panic(err)
	}

	for _, schema := range append(ddl.PreSchemas, ddl.PostSchemas...) {
		schema.fillParentReferences()
		schema.Db = db
	}

	ddl.SourceSchema = sourceSchema
	ddl.TargetSchema = targetSchema

	return &ddl
}

func (d *Ddl) schemaToDiffable(schema []*Schema) []ddldiff.Diffable {
	diff := make([]ddldiff.Diffable, 0)

	for i, _ := range schema {
		diff = append(diff, schema[i])
	}

	return diff
}

func (d *Ddl) filterSchemas(schemas []*Schema) []*Schema {
	for _, schema := range schemas {
		if schema.Name == d.SourceSchema {
			return []*Schema{schema}
		}
	}

	return []*Schema{}
}

func (d *Ddl) Diff() []action.Action {
	actions := ddldiff.Diff(
		d.schemaToDiffable(d.filterSchemas(d.PreSchemas)),
		d.schemaToDiffable(d.filterSchemas(d.PostSchemas)),
		ddldiff.Context{
			Schema: d.TargetSchema,
		},
	)

	// Move indexes to the end of list of actions
	// (FIXME using a decent dependency conflict resolution)
	newActions := make([]action.Action, 0)
	indicesActions := make([]action.Action, 0)

	for _, act := range actions {
		_, isCreateIndex := act.(*action.CreateIndex)
		_, isAlterIndex := act.(*action.AlterIndex)
		_, isDropIndex := act.(*action.DropIndex)

		if isCreateIndex || isAlterIndex || isDropIndex {
			indicesActions = append(indicesActions, act)
		} else {
			newActions = append(newActions, act)
		}
	}

	return append(newActions, indicesActions...)
}
