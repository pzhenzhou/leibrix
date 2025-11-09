package events

import (
	"context"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
)

// Example: Broadcasting a message to all connected workers
func ExampleBroadcastToAllWorkers(sessionManager *SessionManager) {
	// Create a CommonAck event to broadcast (e.g., system announcement)
	payload := map[string]interface{}{
		"message": "System maintenance in 5 minutes",
		"type":    "announcement",
	}

	msg := CreateCommonAckEvent("master-node-1", "system_announcement", payload)

	// Broadcast to all connected workers
	sessionManager.Broadcast(msg)
	logger.Info("Broadcast sent to all workers",
		"active_workers", sessionManager.ActiveWorkerCount())
}

// Example: Sending a DataAssignment to a specific worker
func ExampleSendDataAssignmentToWorker(
	ctx context.Context,
	sessionManager *SessionManager,
	workerID string,
	assignment *myproto.DataAssignmentEvent,
) error {
	// Use the helper method to send data assignment
	if err := sessionManager.SendDataAssignment(workerID, "master-node-1", assignment); err != nil {
		logger.Error(err, "Failed to send assignment to worker",
			"worker_id", workerID,
			"dataset_id", assignment.DatasetId)
		return err
	}

	logger.Info("DataAssignment sent to worker",
		"worker_id", workerID,
		"dataset_id", assignment.DatasetId,
		"epoch_id", assignment.EpochId)
	return nil
}

// Example: Getting list of all connected workers
func ExampleListActiveWorkers(sessionManager *SessionManager) []string {
	workers := sessionManager.ListWorkers()
	logger.Info("Active workers",
		"count", len(workers),
		"worker_ids", workers)
	return workers
}

// Example: Checking if a specific worker is connected
func ExampleCheckWorkerConnection(sessionManager *SessionManager, workerID string) bool {
	if _, ok := sessionManager.Get(workerID); ok {
		logger.Info("Worker is connected", "worker_id", workerID)
		return true
	}
	logger.Info("Worker is not connected", "worker_id", workerID)
	return false
}

// Example: Proactive push from scheduler
// This demonstrates a common pattern where the Master's scheduler
// decides to assign work to a specific worker outside of the
// normal request-response flow
func ExampleSchedulerPushPattern(
	ctx context.Context,
	sessionManager *SessionManager,
	workerID string,
) error {
	// Scheduler decides this worker should get new data
	assignment := &myproto.DataAssignmentEvent{
		DatasetId: "sales_data",
		EpochId:   "1730419200000_a7b3c2",
		LoadPlan: &myproto.LoadPlan{
			PlanId:               "plan_001",
			DestinationTableName: "sales_data__1730419200000_a7b3c2",
			// ... other fields
		},
	}

	// Send the assignment proactively
	return ExampleSendDataAssignmentToWorker(ctx, sessionManager, workerID, assignment)
}
