package transmitter

import (
	"fmt"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/pagarme/teleport/client"
	"github.com/pagarme/teleport/database"
)

type Transmitter struct {
	db      *database.Database
	clients map[string]*client.Client
}

func New(db *database.Database, clients map[string]*client.Client) *Transmitter {
	return &Transmitter{
		db:      db,
		clients: clients,
	}
}

// Transmit batches
func (t *Transmitter) Watch(sleepTime time.Duration) {
	for {
		batches, err := t.db.GetBatches("waiting_transmission", "")

		if err != nil {
			log.Errorf("Error fetching batches for transmission! %v", err)
		} else {
			for _, batch := range batches {
				err := t.transmitBatch(batch)

				if err != nil {
					log.Errorf("Error transmitting batch %s! %v", batch.Id, err)
					break
				}
			}
		}

		batches, err = t.db.GetBatches("transmitted", "waiting_transmission")

		if err != nil {
			log.Errorf("Error fetching batches data for transmission! %v", err)
		} else {
			for _, batch := range batches {
				err := t.transmitBatchData(batch)

				if err != nil {
					log.Errorf("Error transmitting batch %s data! %v", batch.Id, err)
					break
				}
			}
		}

		time.Sleep(sleepTime)
	}
}

// Transmit a batch to a target
func (t *Transmitter) transmitBatch(batch *database.Batch) error {
	client := t.clients[batch.Target]

	if client == nil {
		return fmt.Errorf("could not find client for target '%s'", batch.Target)
	}

	log.Infof("Transmitting batch: %#v", batch)

	err := client.SendRequest("/batches", batch)

	if err != nil {
		return err
	}

	defer log.Infof("Transmitted batch: %#v", batch)

	return t.markBatchTransmitted(batch, false)
}

// Transmit a batch data to a target
func (t *Transmitter) transmitBatchData(batch *database.Batch) error {
	client := t.clients[batch.Target]

	if client == nil {
		return fmt.Errorf("could not find client for target '%s'", batch.Target)
	}

	file, err := batch.GetFile()

	if err != nil {
		return err
	}

	defer file.Close()

	log.Infof("Transmitting batch data: %#v", batch)

	err = client.SendFile(
		fmt.Sprintf("/batches/%s", batch.Id),
		"data",
		file,
	)

	if err != nil {
		return err
	}

	defer log.Infof("Transmitted batch data: %#v", batch)

	return t.markBatchTransmitted(batch, true)
}

func (t *Transmitter) markBatchTransmitted(batch *database.Batch, updateData bool) error {
	// Start transaction
	tx := t.db.NewTransaction()

	// Update batch to transmitted
	batch.Status = "transmitted"
	batch.DataStatus = ""

	if updateData {
		batch.DataStatus = "transmitted"
	}

	err := batch.UpdateQuery(tx)

	if err != nil {
		return err
	}

	// Commit transaction
	return tx.Commit()
}
