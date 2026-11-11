package main

import (
	"strings"
	"time"
	_ "time/tzdata" // 容器缺少系统时区文件时也可解析宿主 IANA 时区。
)

func (s *Store) SetTimeZone(name string) error {
	location := time.Local
	if name = strings.TrimSpace(name); name != "" {
		var err error
		location, err = time.LoadLocation(name)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.location = location
	s.mu.Unlock()
	return nil
}

func (s *Store) locationLocked() *time.Location {
	if s.location != nil {
		return s.location
	}
	return time.Local
}

func (s *Store) Location() *time.Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.locationLocked()
}
