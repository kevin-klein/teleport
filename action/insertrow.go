package action

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

type InsertRow struct {
	SchemaName     string
	TableName      string
	PrimaryKeyName string
	Rows           Rows
	BulkInsert     bool
}

// Register type for gob
func init() {
	gob.Register(&InsertRow{})
	gob.Register(&time.Time{})
}

func (a *InsertRow) Execute(c *Context) error {
	if a.BulkInsert {
		escapedCols := make([]string, 0)
		values := make([]interface{}, 0)

		for _, row := range a.Rows {
			escapedCols = append(escapedCols, row.Column.Name)
			values = append(values, row.GetValue())
		}

		stmt, err := c.GetPreparedStatement(
			pq.CopyInSchema(a.SchemaName, a.TableName, escapedCols...),
		)

		if err != nil {
			return err
		}

		_, err = stmt.Exec(values...)

		if err != nil {
			return err
		}
	} else {
		// Perform a single insert (upsert)
		escapedCols := make([]string, 0)
		escapedRows := make([]string, 0)
		values := make([]interface{}, 0)

		var primaryKeyRow Row

		for i, row := range a.Rows {
			escapedCols = append(escapedCols, fmt.Sprintf("\"%s\"", row.Column.Name))
			escapedRows = append(escapedRows, fmt.Sprintf("$%d::%s\"%s\"", i+1, row.Column.GetTypeSchemaStr(a.SchemaName), row.Column.Type))

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

			if row.Column.Name == a.PrimaryKeyName {
				primaryKeyRow = row
			}
		}

		// Save transaction prior to inserting to rollback
		// if INSERT fails, so a UPDATE can be tried
		_, err := c.Tx.Exec(fmt.Sprintf(
			`SAVEPOINT "%s%s";`,
			a.SchemaName,
			a.TableName,
		))

		if err != nil {
			return err
		}

		_, err = c.Tx.Exec(
			fmt.Sprintf(
				`
					INSERT INTO "%s"."%s" (%s) VALUES (%s);
				`,
				a.SchemaName,
				a.TableName,
				strings.Join(escapedCols, ","),
				strings.Join(escapedRows, ","),
			),
			values...,
		)

		// Try to UPDATE (upsert) if INSERT fails...
		if err != nil {
			// Rollback to SAVEPOINT
			_, err = c.Tx.Exec(fmt.Sprintf(
				`ROLLBACK TO SAVEPOINT "%s%s";`,
				a.SchemaName,
				a.TableName,
			))

			if err != nil {
				return err
			}

			updateAction := &UpdateRow{
				a.SchemaName,
				a.TableName,
				primaryKeyRow,
				a.Rows,
			}

			err = updateAction.Execute(c)

			if err != nil {
				return err
			}
		} else {
			// Release SAVEPOINT to avoid "out of shared memory"
			_, err := c.Tx.Exec(fmt.Sprintf(
				`RELEASE SAVEPOINT "%s%s";`,
				a.SchemaName,
				a.TableName,
			))

			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (a *InsertRow) Filter(targetExpression string) bool {
	return IsInTargetExpression(&targetExpression, &a.SchemaName, &a.TableName)
}

func (a *InsertRow) NeedsSeparatedBatch() bool {
	return false
}
