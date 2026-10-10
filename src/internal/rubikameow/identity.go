package rubikameow

func (Contract) ValidateMessageID(id string) bool { return validMessageID(id) }
