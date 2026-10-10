package usecase

import "github.com/mimalef70/goomni/src/infrastructure/storage"

func (s *Service) admissionLimits() storage.AdmissionLimits {
	return storage.AdmissionLimits{Global: s.options.QueueLimit, Connection: s.options.ConnectionQueueLimit}
}
