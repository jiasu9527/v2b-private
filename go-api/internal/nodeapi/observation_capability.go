package nodeapi

import (
	"context"
	"time"
)

// RecordObservationCapability tracks which node build actually fetched its
// config. A short freshness window prevents a downgraded/offline node from
// being counted as currently collecting merely because it did so once.
func (s *DBService) RecordObservationCapability(ctx context.Context, nodeType string, nodeID int64, capability string) error {
	if err := validateObservationNode(nodeType, nodeID); err != nil {
		return err
	}
	if capability != "auto-v2" && capability != "all-v1" && capability != "legacy" {
		return ErrObservationInvalid
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_node_observation_capability(node_type,node_id,capability,last_seen_at)
VALUES('v2node',$1,$2,$3)
ON CONFLICT(node_type,node_id) DO UPDATE SET capability=EXCLUDED.capability,last_seen_at=EXCLUDED.last_seen_at
WHERE v2_node_observation_capability.capability<>EXCLUDED.capability OR v2_node_observation_capability.last_seen_at<EXCLUDED.last_seen_at-30`,
		nodeID, capability, time.Now().Unix())
	return err
}
