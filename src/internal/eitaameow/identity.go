package eitaameow

func (Contract) ValidateMessageID(id string) bool {
	_, err := messageNumber(id)
	return err == nil
}
