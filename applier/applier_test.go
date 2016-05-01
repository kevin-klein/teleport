package applier

import (
	"encoding/gob"
	"fmt"
	"github.com/pagarme/teleport/action"
	"github.com/pagarme/teleport/client"
	"github.com/pagarme/teleport/config"
	"github.com/pagarme/teleport/database"
	"os"
	"testing"
)

var db *database.Database
var stubBatch *database.Batch
var applier *Applier

func init() {
	gob.Register(&StubAction{})

	config := config.New()
	err := config.ReadFromFile("../config_test.yml")

	if err != nil {
		fmt.Printf("Error opening config file: %v\n", err)
		os.Exit(1)
	}

	db = database.New(
		config.Database.Name,
		config.Database.Database,
		config.Database.Hostname,
		config.Database.Username,
		config.Database.Password,
		config.Database.Port,
	)

	// Start db
	if err = db.Start(); err != nil {
		fmt.Printf("Erro starting database: ", err)
		os.Exit(1)
	}

	stubAction := &StubAction{}

	stubBatch = &database.Batch{
		Id:          "",
		Status:      "waiting_apply",
		DataStatus:  "waiting_apply",
		Source:      "source",
		Target:      "target",
		Data:        nil,
		StorageType: "db",
	}

	stubBatch.SetActions([]action.Action{stubAction})

	targets := make(map[string]*client.Client)

	for key, target := range config.Targets {
		targets[key] = client.New(target)
	}

	applier = New(db, 100)
}

// StubAction implements Action
type StubAction struct{}

func (a *StubAction) Execute(c *action.Context) error {
	_, err := c.Tx.Exec("CREATE TABLE test (id INT); INSERT INTO test (id) VALUES (3);")
	return err
}

func (a *StubAction) Filter(targetExpression string) bool {
	return true
}

func (a *StubAction) NeedsSeparatedBatch() bool {
	return false
}

func TestApplyBatch(t *testing.T) {
	db.Db.Exec(`
		DROP TABLE test;
		TRUNCATE teleport.batch;
	`)

	tx := db.NewTransaction()
	stubBatch.InsertQuery(tx)
	tx.Commit()

	_, err := applier.applyBatch(stubBatch)

	if err != nil {
		t.Errorf("applyBatch returned error: %v", err)
	}

	var testId string
	db.Db.Get(&testId, "SELECT id FROM test;")

	if testId != "3" {
		t.Errorf("test id => %s, want %s", testId, "3")
	}

	batches, _ := db.GetBatches("applied", "")

	if len(batches) != 1 {
		t.Errorf("applied batches => %d, want %d", len(batches), 1)
	}
}
