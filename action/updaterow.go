package action

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

type UpdateRow struct {
	SchemaName string
	TableName  string
	PrimaryKey Row
	Rows       []Row
}

// Register type for gob
func init() {
	gob.Register(&UpdateRow{})
}

func (a *UpdateRow) Execute(c *Context) error {
	escapedRows := make([]string, 0)
	replacementsCount := 0
	values := make([]interface{}, 0)

	for _, row := range a.Rows {
		replacementsCount++
		escapedRows = append(escapedRows, fmt.Sprintf("\"%s\" = $%d::%s\"%s\"", row.Column.Name, replacementsCount, row.Column.GetTypeSchemaStr(a.SchemaName), row.Column.Type))

		// Marshall JSON objects as pg driver does not support it
		if obj, ok := row.Value.(*map[string]interface{}); ok {
			jsonStr, err := json.Marshal(obj)

			if err != nil {
				return err
			}

			values = append(values, string(jsonStr))
		} else {
			switch row.Value.(type) {
			default:
				values = append(values, row.Value)
			case []interface{}:
				values = append(values, pq.Array(row.Value))
			}
		}
	}

	replacementsCount++

	_, err := c.Tx.Exec(
		fmt.Sprintf(
			`UPDATE "%s"."%s" SET %s WHERE "%s" = $%d;`,
			a.SchemaName,
			a.TableName,
			strings.Join(escapedRows, ", "),
			a.PrimaryKey.Column.Name,
			replacementsCount,
		),
		append(values, a.PrimaryKey.Value)...,
	)

	return err
}

func (a *UpdateRow) Filter(targetExpression string) bool {
	return IsInTargetExpression(&targetExpression, &a.SchemaName, &a.TableName)
}

func (a *UpdateRow) NeedsSeparatedBatch() bool {
	return false
}
