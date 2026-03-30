package user

import "encoding/json"

func subscribeAliveIPCount(raw string) int64 {
	var state map[string]any
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return 0
	}
	return mapInt64(state["alive_ip"])
}
