package config

import "sync"

type RuntimeValues struct {
	AllowNewPeriod             bool
	ResetTrafficMethod         int64
	CommissionAutoCheckEnable  bool
	CommissionAutoCheckMinutes int64
	OrderKeepDays              int64
	MailLogKeepDays            int64
	LogKeepDays                int64
	StatUserKeepDays           int64
	StatServerKeepDays         int64
	AuthSessionKeepDays        int64
	RuntimeKVKeepDays          int64
	FailedJobsKeepDays         int64
}

type RuntimeState struct {
	base   Config
	mu     sync.RWMutex
	values RuntimeValues
	cfg    Config
}

func NewRuntimeState(base Config) *RuntimeState {
	state := &RuntimeState{base: base, cfg: base}
	state.Reload()
	return state
}

func (s *RuntimeState) Reload() RuntimeValues {
	if s == nil {
		return RuntimeValues{}
	}

	cfg := Load()
	values := loadRuntimeValues(cfg)
	s.mu.Lock()
	s.cfg = cfg
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

func (s *RuntimeState) CurrentConfig() Config {
	if s == nil {
		return Config{}
	}

	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	return cfg
}

func loadRuntimeValues(cfg Config) RuntimeValues {
	return RuntimeValues{
		AllowNewPeriod:             cfg.AllowNewPeriod,
		ResetTrafficMethod:         cfg.ResetTrafficMethod,
		CommissionAutoCheckEnable:  cfg.CommissionAutoCheckEnable,
		CommissionAutoCheckMinutes: cfg.CommissionAutoCheckMinutes,
		OrderKeepDays:              cfg.OrderKeepDays,
		MailLogKeepDays:            cfg.MailLogKeepDays,
		LogKeepDays:                cfg.LogKeepDays,
		StatUserKeepDays:           cfg.StatUserKeepDays,
		StatServerKeepDays:         cfg.StatServerKeepDays,
		AuthSessionKeepDays:        cfg.AuthSessionKeepDays,
		RuntimeKVKeepDays:          cfg.RuntimeKVKeepDays,
		FailedJobsKeepDays:         cfg.FailedJobsKeepDays,
	}
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
