package balemeow

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"unicode/utf8"

	"github.com/mimalef70/gobale/src/domains"
)

// Legacy JSON union7 is not an arbitrary JSON passthrough. Only the official
// contact/location shapes are projected; unfamiliar keys and remote photo URLs
// never become consumer-controlled actions or network fetches.
func jsonContentBody(raw string) map[string]any {
	unsupported := map[string]any{"kind": "unsupported", "provider_kind": "json"}
	if len(raw) > 64<<10 || !utf8.ValidString(raw) || domains.ValidateJSONObject([]byte(raw)) != nil {
		return unsupported
	}
	var env struct {
		DataType string `json:"dataType"`
		Data     struct {
			Contact *struct {
				Name   string   `json:"name"`
				Phones []string `json:"phones"`
				Emails []string `json:"emails"`
			} `json:"contact"`
			Location *struct {
				Latitude  *float64 `json:"latitude"`
				Longitude *float64 `json:"longitude"`
			} `json:"location"`
		} `json:"data"`
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	if d.Decode(&env) != nil || d.Decode(new(any)) != io.EOF {
		return unsupported
	}
	switch env.DataType {
	case "contact":
		c := env.Data.Contact
		if c == nil || len(c.Name) > 4096 || len(c.Phones) > 100 || len(c.Emails) > 100 {
			return unsupported
		}
		for _, v := range c.Phones {
			if len(v) > 128 {
				return unsupported
			}
		}
		for _, v := range c.Emails {
			if len(v) > 1024 {
				return unsupported
			}
		}
		return map[string]any{"kind": "contact", "name": c.Name, "phones": c.Phones, "emails": c.Emails}
	case "location":
		l := env.Data.Location
		if l == nil || l.Latitude == nil || l.Longitude == nil {
			return unsupported
		}
		lat, lon := *l.Latitude, *l.Longitude
		if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return unsupported
		}
		return map[string]any{"kind": "location", "latitude": lat, "longitude": lon}
	}
	return unsupported
}
