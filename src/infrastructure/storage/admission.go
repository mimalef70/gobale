package storage

// AdmissionLimits bound durable outstanding work. Unknown outcomes retain their
// slot until explicitly reconciled; they must never be discarded to make room.
type AdmissionLimits struct {
	Global     int
	Connection int
}

func (v AdmissionLimits) normalized() AdmissionLimits {
	if v.Global <= 0 {
		v.Global = 1000
	}
	if v.Connection <= 0 {
		v.Connection = 100
	}
	return v
}
