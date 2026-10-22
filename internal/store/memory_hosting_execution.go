package store

import "nerocd/internal/domain"

// Executable hosting needs PostgreSQL and River. Even corrupt or manually
// seeded memory fixtures must never manufacture targetless image authority.
func (s *MemoryStore) hostingRunLocked(id string) bool {
	for _, run := range s.runs {
		if run.ID == id && run.RunSpec.Type == domain.RunTypeImageDeploy {
			return true
		}
	}
	return false
}
