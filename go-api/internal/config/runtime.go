package config

import "sync"

type RuntimeValues struct {
	AllowNewPeriod     bool
	ResetTrafficMethod int64
}

type RuntimeState struct {
	base   Config
	mu     sync.RWMutex
	values RuntimeValues
}

func NewRuntimeState(base Config) *RuntimeState {
	state := &RuntimeState{base: base}
	state.Reload()
	return state
}

func (s *RuntimeState) Reload() RuntimeValues {
	if s == nil {
		return RuntimeValues{}
	}

	values := loadRuntimeValues(s.base)
	s.mu.Lock()
	s.values = values
	s.mu.Unlock()
	return values
}

func (s *RuntimeState) Current() RuntimeValues {
	if s == nil {
		return RuntimeValues{}
	}

	s.mu.RLock()
	values := s.values
	s.mu.RUnlock()
	return values
}

func loadRuntimeValues(base Config) RuntimeValues {
	jsonConfig := loadJSONConfigMap(defaultAdminJSONPath)
	return RuntimeValues{
		AllowNewPeriod: getEnvBool("ALLOW_NEW_PERIOD", loadConfigInt64(jsonConfig, defaultLegacyPHPConfigPath, "allow_new_period", boolToInt64(base.AllowNewPeriod)) != 0),
		ResetTrafficMethod: getEnvInt64(
			"RESET_TRAFFIC_METHOD",
			loadConfigInt64(jsonConfig, defaultLegacyPHPConfigPath, "reset_traffic_method", base.ResetTrafficMethod),
		),
	}
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
