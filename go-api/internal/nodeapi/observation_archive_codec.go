package nodeapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	maxObservationArchiveEvents = 10000
	maxObservationArchiveBytes  = 32 << 20
	observationArchiveVersion   = 1
	observationArchiveBloomK    = 10
)

// The archive is a lossless versioned representation, not an aggregate. Keep
// every original event field, including legacy identifier spelling and times.
type archivedObservationEvent struct {
	_msgpack      struct{} `msgpack:",as_array"`
	NodeType      string
	NodeID        int64
	EventID       string
	UserID        int64
	OccurredAt    int64
	ReceivedAt    int64
	ClientIP      string
	TargetHost    string
	TargetIP      string
	TargetPort    int
	Network       string
	Result        string
	UplinkBytes   int64
	DownlinkBytes int64
}

type observationArchiveBlock struct {
	ID            int64
	HourAt        int64
	UserID        int64
	FirstAt       int64
	LastAt        int64
	EventCount    int64
	KnownEvents   int64
	Payload       []byte
	PayloadSHA256 []byte
	EventBloom    []byte
	NodeIDs       []int64
	TargetHosts   []string
	TargetIPs     []string
}

func observationArchiveMetadata(events []archivedObservationEvent) (observationArchiveBlock, error) {
	var block observationArchiveBlock
	if len(events) == 0 || len(events) > maxObservationArchiveEvents {
		return block, fmt.Errorf("archive must contain 1..%d events", maxObservationArchiveEvents)
	}
	first := events[0]
	if first.UserID < 1 || first.OccurredAt < 0 {
		return block, fmt.Errorf("invalid archive user or timestamp")
	}
	block.UserID, block.HourAt = first.UserID, first.OccurredAt/3600*3600
	block.FirstAt, block.LastAt = first.OccurredAt, first.OccurredAt
	block.EventCount = int64(len(events))
	block.EventBloom = make([]byte, max(64, len(events)*4))
	nodes, hosts, ips := map[int64]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, event := range events {
		if event.UserID != block.UserID || event.OccurredAt < block.HourAt || event.OccurredAt >= block.HourAt+3600 {
			return observationArchiveBlock{}, fmt.Errorf("archive events must share a user and UTC hour")
		}
		if event.NodeID < 1 || event.EventID == "" {
			return observationArchiveBlock{}, fmt.Errorf("archive event has no node or identifier")
		}
		block.FirstAt, block.LastAt = min(block.FirstAt, event.OccurredAt), max(block.LastAt, event.OccurredAt)
		nodes[event.NodeID] = struct{}{}
		if event.TargetHost != "" {
			hosts[event.TargetHost] = struct{}{}
			block.KnownEvents++
		}
		if event.TargetIP != "" {
			ips[event.TargetIP] = struct{}{}
		}
		observationArchiveBloomAdd(block.EventBloom, event.NodeID, event.EventID)
	}
	for id := range nodes {
		block.NodeIDs = append(block.NodeIDs, id)
	}
	for host := range hosts {
		block.TargetHosts = append(block.TargetHosts, host)
	}
	for ip := range ips {
		block.TargetIPs = append(block.TargetIPs, ip)
	}
	// Empty arrays must serialize as SQL arrays rather than NULL metadata.
	if block.NodeIDs == nil {
		block.NodeIDs = []int64{}
	}
	if block.TargetHosts == nil {
		block.TargetHosts = []string{}
	}
	if block.TargetIPs == nil {
		block.TargetIPs = []string{}
	}
	sort.Slice(block.NodeIDs, func(i, j int) bool { return block.NodeIDs[i] < block.NodeIDs[j] })
	sort.Strings(block.TargetHosts)
	sort.Strings(block.TargetIPs)
	return block, nil
}

func encodeObservationArchive(events []archivedObservationEvent) (observationArchiveBlock, error) {
	block, err := observationArchiveMetadata(events)
	if err != nil {
		return block, err
	}
	var plain bytes.Buffer
	encoder := msgpack.NewEncoder(&plain)
	if err = encoder.EncodeArrayLen(2); err == nil {
		err = encoder.EncodeInt(observationArchiveVersion)
	}
	if err == nil {
		err = encoder.EncodeArrayLen(len(events))
	}
	for i := range events {
		if err != nil {
			break
		}
		err = encoder.Encode(&events[i])
		if plain.Len() > maxObservationArchiveBytes {
			return observationArchiveBlock{}, fmt.Errorf("archive exceeds uncompressed byte limit")
		}
	}
	if err != nil {
		return observationArchiveBlock{}, fmt.Errorf("encode observation archive: %w", err)
	}
	var packed bytes.Buffer
	writer := gzip.NewWriter(&packed)
	if _, err = writer.Write(plain.Bytes()); err != nil {
		writer.Close()
		return observationArchiveBlock{}, err
	}
	if err = writer.Close(); err != nil {
		return observationArchiveBlock{}, err
	}
	block.Payload = packed.Bytes()
	digest := sha256.Sum256(block.Payload)
	block.PayloadSHA256 = append([]byte(nil), digest[:]...)
	return block, nil
}

func decodeObservationArchive(payload, digest []byte) ([]archivedObservationEvent, error) {
	if len(payload) == 0 || len(payload) > maxObservationArchiveBytes || len(digest) != sha256.Size {
		return nil, fmt.Errorf("invalid observation archive length or checksum")
	}
	actual := sha256.Sum256(payload)
	if !bytes.Equal(actual[:], digest) {
		return nil, fmt.Errorf("observation archive checksum mismatch")
	}
	source := bytes.NewReader(payload)
	reader, err := gzip.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("open observation archive: %w", err)
	}
	reader.Multistream(false)
	plain, readErr := io.ReadAll(io.LimitReader(reader, maxObservationArchiveBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read observation archive: %w", firstArchiveError(readErr, closeErr))
	}
	if len(plain) > maxObservationArchiveBytes || source.Len() != 0 {
		return nil, fmt.Errorf("observation archive is oversized or contains trailing data")
	}
	unpacked := bytes.NewReader(plain)
	decoder := msgpack.NewDecoder(unpacked)
	n, err := decoder.DecodeArrayLen()
	if err != nil || n != 2 {
		return nil, fmt.Errorf("invalid observation archive envelope")
	}
	version, err := decoder.DecodeInt64()
	if err != nil || version != observationArchiveVersion {
		return nil, fmt.Errorf("unsupported observation archive version")
	}
	n, err = decoder.DecodeArrayLen()
	if err != nil || n < 1 || n > maxObservationArchiveEvents {
		return nil, fmt.Errorf("invalid observation archive event count")
	}
	events := make([]archivedObservationEvent, n)
	for i := range events {
		if err = decoder.Decode(&events[i]); err != nil {
			return nil, fmt.Errorf("decode observation archive event: %w", err)
		}
	}
	if unpacked.Len() != 0 {
		return nil, fmt.Errorf("trailing observation archive message")
	}
	userID, hour := events[0].UserID, events[0].OccurredAt/3600*3600
	if userID < 1 || events[0].OccurredAt < 0 {
		return nil, fmt.Errorf("invalid archived user or timestamp")
	}
	for _, event := range events {
		if event.UserID != userID || event.OccurredAt < hour || event.OccurredAt >= hour+3600 || event.NodeID < 1 || event.EventID == "" {
			return nil, fmt.Errorf("invalid archived event or mixed user/hour")
		}
	}
	return events, nil
}

func firstArchiveError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func observationArchiveBloomHash(nodeID int64, eventID string) (uint64, uint64) {
	var node [8]byte
	binary.BigEndian.PutUint64(node[:], uint64(nodeID))
	h := sha256.New()
	h.Write(node[:])
	h.Write([]byte(eventID))
	digest := h.Sum(nil)
	return binary.BigEndian.Uint64(digest[:8]), binary.BigEndian.Uint64(digest[8:16]) | 1
}

func observationArchiveBloomAdd(bloom []byte, nodeID int64, eventID string) {
	a, b := observationArchiveBloomHash(nodeID, eventID)
	bits := uint64(len(bloom)) * 8
	for i := uint64(0); i < observationArchiveBloomK; i++ {
		index := (a + i*b) % bits
		bloom[index/8] |= 1 << (index % 8)
	}
}

// Bloom filters are only an optimization. Invalid metadata must force an exact
// lookup, never silently declare that an event ID cannot already be archived.
func observationArchiveMayContain(bloom []byte, nodeID int64, eventID string) bool {
	if len(bloom) < 64 || len(bloom) > maxObservationArchiveEvents*4 {
		return true
	}
	a, b := observationArchiveBloomHash(nodeID, eventID)
	bits := uint64(len(bloom)) * 8
	for i := uint64(0); i < observationArchiveBloomK; i++ {
		index := (a + i*b) % bits
		if bloom[index/8]&(1<<(index%8)) == 0 {
			return false
		}
	}
	return true
}
