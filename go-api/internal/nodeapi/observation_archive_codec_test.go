package nodeapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func archiveCodecEvent(id string, at int64) archivedObservationEvent {
	return archivedObservationEvent{NodeType: "v2node", NodeID: 7, EventID: id, UserID: 10, OccurredAt: at,
		ReceivedAt: at + 10, ClientIP: "2001:db8::123", TargetHost: "www.example.test", TargetIP: "198.51.100.1",
		TargetPort: 443, Network: "tcp", Result: "route_attempt"}
}

func TestObservationArchiveCodecLosslessFieldsAndMetadata(t *testing.T) {
	const hour = 1795014000
	events := []archivedObservationEvent{
		archiveCodecEvent("0123456789abcdef0123456789abcdef", hour+90),
		archiveCodecEvent("0123456789ABCDEF0123456789ABCDEF", hour+2),
		archiveCodecEvent("legacy_mixed-Case123", hour+3599),
	}
	events[1].NodeID, events[1].Network = 8, "udp"
	events[1].ClientIP, events[1].TargetHost = "", ""
	// Older records must round-trip unchanged, not be rewritten as constants.
	events[2].Result, events[2].UplinkBytes, events[2].DownlinkBytes = "legacy_result", 7, 13
	block, err := encodeObservationArchive(events)
	if err != nil {
		t.Fatal(err)
	}
	if block.HourAt != hour || block.UserID != 10 || block.FirstAt != hour+2 || block.LastAt != hour+3599 || block.EventCount != 3 || block.KnownEvents != 2 {
		t.Fatalf("incorrect block metadata: %#v", block)
	}
	if !reflect.DeepEqual(block.NodeIDs, []int64{7, 8}) || !reflect.DeepEqual(block.TargetHosts, []string{"www.example.test"}) || !reflect.DeepEqual(block.TargetIPs, []string{"198.51.100.1"}) {
		t.Fatalf("incorrect index projections: %#v", block)
	}
	actual, err := decodeObservationArchive(block.Payload, block.PayloadSHA256)
	if err != nil || !reflect.DeepEqual(actual, events) {
		t.Fatalf("lossless round-trip failed: got=%#v err=%v", actual, err)
	}
	for _, event := range events {
		if !observationArchiveMayContain(block.EventBloom, event.NodeID, event.EventID) {
			t.Fatalf("Bloom false negative: %s", event.EventID)
		}
	}
}

func TestObservationArchiveCodecRejectsMixedAndInvalidBlocks(t *testing.T) {
	const hour = 1795014000
	valid := archiveCodecEvent("event_123", hour)
	for _, kind := range []string{"empty", "too_many", "mixed_user", "mixed_hour", "no_node", "no_id", "negative_user", "negative_timestamp"} {
		t.Run(kind, func(t *testing.T) {
			events := []archivedObservationEvent{valid, valid}
			switch kind {
			case "empty":
				events = nil
			case "too_many":
				events = make([]archivedObservationEvent, maxObservationArchiveEvents+1)
			case "mixed_user":
				events[1].UserID++
			case "mixed_hour":
				events[1].OccurredAt += 3600
			case "no_node":
				events[1].NodeID = 0
			case "no_id":
				events[1].EventID = ""
			case "negative_user":
				events[0].UserID = -1
			case "negative_timestamp":
				events[0].OccurredAt = -1
			}
			if _, err := encodeObservationArchive(events); err == nil {
				t.Fatal("invalid block accepted")
			}
		})
	}
}

func packArchiveTestBytes(t *testing.T, raw []byte) ([]byte, []byte) {
	t.Helper()
	var packed bytes.Buffer
	z := gzip.NewWriter(&packed)
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	payload := packed.Bytes()
	digest := sha256.Sum256(payload)
	return payload, digest[:]
}

func TestObservationArchiveCodecDetectsCorruptionAndInvalidEnvelope(t *testing.T) {
	block, err := encodeObservationArchive([]archivedObservationEvent{archiveCodecEvent("event_123", 1795014000)})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"wrong_hash", "crc_failure", "truncated", "trailing_gzip", "unknown_version", "too_many", "empty", "trailing_message"} {
		t.Run(kind, func(t *testing.T) {
			payload, digest := append([]byte(nil), block.Payload...), append([]byte(nil), block.PayloadSHA256...)
			switch kind {
			case "wrong_hash":
				digest[0] ^= 1
			case "crc_failure":
				payload[len(payload)-8] ^= 1
				d := sha256.Sum256(payload)
				digest = d[:]
			case "truncated":
				payload = payload[:len(payload)-8]
				d := sha256.Sum256(payload)
				digest = d[:]
			case "trailing_gzip":
				payload = append(payload, block.Payload...)
				d := sha256.Sum256(payload)
				digest = d[:]
			default:
				var raw bytes.Buffer
				enc := msgpack.NewEncoder(&raw)
				enc.EncodeArrayLen(2)
				version, count := 1, 0
				if kind == "unknown_version" {
					version = 2
				} else if kind == "too_many" {
					count = maxObservationArchiveEvents + 1
				} else if kind == "trailing_message" {
					count = 1
				}
				enc.EncodeInt(int64(version))
				enc.EncodeArrayLen(count)
				if kind == "trailing_message" {
					valid := archiveCodecEvent("event_123", 1795014000)
					enc.Encode(&valid)
					enc.EncodeInt(99)
				}
				payload, digest = packArchiveTestBytes(t, raw.Bytes())
			}
			if _, err := decodeObservationArchive(payload, digest); err == nil {
				t.Fatal("corrupt archive accepted")
			}
		})
	}
}

func TestObservationArchiveCodecBoundsExpansion(t *testing.T) {
	oversized := bytes.Repeat([]byte{'x'}, maxObservationArchiveBytes+1)
	payload, digest := packArchiveTestBytes(t, oversized)
	if _, err := decodeObservationArchive(payload, digest); err == nil {
		t.Fatal("unbounded decompression accepted")
	}
	event := archiveCodecEvent("event_123", 1795014000)
	event.TargetHost = string(oversized)
	if _, err := encodeObservationArchive([]archivedObservationEvent{event}); err == nil {
		t.Fatal("oversized encoding accepted")
	}
}

func TestObservationArchiveBloomNoFalseNegativesAndNodeScope(t *testing.T) {
	events := make([]archivedObservationEvent, maxObservationArchiveEvents)
	for i := range events {
		d := sha256.Sum256([]byte(fmt.Sprintf("event_%d", i)))
		events[i] = archiveCodecEvent(hex.EncodeToString(d[:16]), 1795014000+int64(i%3600))
	}
	block, err := encodeObservationArchive(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if !observationArchiveMayContain(block.EventBloom, event.NodeID, event.EventID) {
			t.Fatal("Bloom false negative")
		}
	}
	falsePositives := 0
	for i := range events {
		if observationArchiveMayContain(block.EventBloom, 8, events[i].EventID) {
			falsePositives++
		}
	}
	if falsePositives > 10 {
		t.Fatalf("unexpected Bloom false-positive rate: %d/10000", falsePositives)
	}
	if !observationArchiveMayContain(nil, 7, "event_123") || !observationArchiveMayContain([]byte{0}, 7, "event_123") {
		t.Fatal("invalid Bloom must require exact lookup")
	}
	decoded, err := decodeObservationArchive(block.Payload, block.PayloadSHA256)
	if err != nil || len(decoded) != len(events) {
		t.Fatalf("maximum-sized block failed: %d %v", len(decoded), err)
	}
	if bytes.Contains(block.Payload, []byte(strings.Repeat("event_", 3))) {
		t.Fatal("archive payload was not compressed")
	}
}
