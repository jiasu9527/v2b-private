package aiops

import (
	"context"

	"forest/go-api/internal/admin"
)

// Keep the panel dependency limited to the existing operational reads. Payment
// configuration and general configuration methods are deliberately absent.
// The concrete value is retained so narrow optional operational interfaces
// (probes, ticket settings, cache invalidation) remain available.
type Panel interface {
	GetSystemStatus(context.Context) (admin.SystemStatus, error)
	GetQueueStats(context.Context) (admin.QueueStats, error)
	ListUsers(context.Context, admin.UserFetchRequest) (admin.UserListResult, error)
	FetchOrders(context.Context, admin.OrderFetchRequest) (admin.OrderListResult, error)
	ListManagedServers(context.Context) ([]map[string]any, error)
	ListClientEntryUserPolicies(context.Context) ([]admin.ClientEntryUserPolicyRecord, error)
	SimulateClientEntryUserPolicy(context.Context, admin.ClientEntryUserPolicySimulationRequest) (admin.ClientEntryUserPolicySimulationResult, error)
}
