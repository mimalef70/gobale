package sqlite

func errorCategory(code int) string {
	switch code {
	case 5:
		return "busy"
	case 6:
		return "locked"
	case 8:
		return "readonly"
	case 10:
		return "io"
	case 11, 26:
		return "corrupt"
	case 13:
		return "full"
	case 19:
		return "constraint"
	default:
		return "other"
	}
}
