package balemeow

import "strconv"

func eventActor(account string, id int64) (string, string) {
	if id <= 0 || id > 1<<32-1 {
		return "", "unknown"
	}
	sender := strconv.FormatUint(uint64(id), 10)
	if sender == account {
		return sender, "outgoing"
	}
	return sender, "incoming"
}
