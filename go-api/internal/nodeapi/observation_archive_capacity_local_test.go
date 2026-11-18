package nodeapi

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"testing"
)

// Optional read-only reproduction against a captured CSV sample. It prints
// aggregate sizes only, never user IDs, source addresses or destinations.
func TestObservationArchiveCapturedSampleCapacity(t *testing.T) {
	path := os.Getenv("OBSERVATION_SAMPLE_CSV_GZ")
	if path == "" {
		t.Skip("OBSERVATION_SAMPLE_CSV_GZ not set")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	packed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer packed.Close()
	reader := csv.NewReader(packed)
	if _, err := reader.Read(); err != nil {
		t.Fatal(err)
	}
	type key struct{ user, hour int64 }
	groups := map[key][]archivedObservationEvent{}
	var total int64
	for {
		r, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		integer := func(s string) int64 {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		var id string
		eventKey, err := hex.DecodeString(r[4])
		if err != nil || len(eventKey) < 2 {
			t.Fatalf("invalid event key: %v", err)
		}
		if eventKey[0] == 0 {
			id = hex.EncodeToString(eventKey[1:])
		} else {
			id = string(eventKey[1:])
		}
		network := "tcp"
		if r[9] == "1" {
			network = "udp"
		}
		event := archivedObservationEvent{NodeType: "v2node", NodeID: integer(r[0]), UserID: integer(r[1]),
			OccurredAt: integer(r[2]), ReceivedAt: integer(r[3]), EventID: id,
			ClientIP: r[5], TargetHost: r[6], TargetIP: r[7], TargetPort: int(integer(r[8])), Network: network, Result: "route_attempt"}
		group := key{event.UserID, event.OccurredAt / 3600}
		groups[group] = append(groups[group], event)
		total++
	}
	var payload, metadata, blocks int64
	var encoded []observationArchiveBlock
	for _, events := range groups {
		for len(events) > 0 {
			n := min(len(events), maxObservationArchiveEvents)
			block, err := encodeObservationArchive(events[:n])
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, block)
			payload += int64(len(block.Payload))
			metadata += int64(len(block.EventBloom)) + 120 + int64(len(block.NodeIDs))*8
			for _, host := range block.TargetHosts {
				metadata += int64(len(host)) + 4
			}
			for _, ip := range block.TargetIPs {
				metadata += int64(len(ip)) + 4
			}
			blocks++
			events = events[n:]
		}
	}
	t.Logf("events=%d blocks=%d gzip_payload_bytes=%d metadata_estimated_bytes=%d payload_per_event=%.2f payload_metadata_per_event=%.2f (excludes SQL/GIN index overhead)", total, blocks, payload, metadata, float64(payload)/float64(total), float64(payload+metadata)/float64(total))
	if os.Getenv("OBSERVATION_TEST_DSN") != "" || os.Getenv("FOREST_OBSERVATION_TEST_DSN") != "" {
		db, _, _ := observationPostgresFixture(t)
		var before, after int64
		if err := db.QueryRow(`SELECT pg_total_relation_size('v2_node_observation_archive')`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for _, b := range encoded {
			nodes, _ := json.Marshal(b.NodeIDs)
			hosts, _ := json.Marshal(b.TargetHosts)
			ips, _ := json.Marshal(b.TargetIPs)
			if _, err := db.Exec(`INSERT INTO v2_node_observation_archive(hour_at,user_id,first_at,last_at,event_count,known_events,payload,payload_sha256,event_bloom,node_ids,target_hosts,target_ips)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,ARRAY(SELECT value::bigint FROM jsonb_array_elements_text($10::jsonb)),ARRAY(SELECT value FROM jsonb_array_elements_text($11::jsonb)),ARRAY(SELECT value FROM jsonb_array_elements_text($12::jsonb)))`, b.HourAt, b.UserID, b.FirstAt, b.LastAt, b.EventCount, b.KnownEvents, b.Payload, b.PayloadSHA256, b.EventBloom, string(nodes), string(hosts), string(ips)); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.QueryRow(`SELECT pg_total_relation_size('v2_node_observation_archive')`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		t.Logf("postgres_total_bytes=%d empty_baseline_bytes=%d delta_bytes=%d delta_bytes_per_event=%.2f (includes TOAST and all indexes)", after, before, after-before, float64(after-before)/float64(total))
	}
}
