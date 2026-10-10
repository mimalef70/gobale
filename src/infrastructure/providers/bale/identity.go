package bale

func (Contract) ValidateMessageID(id string) bool { return ValidMessageID(id) }
