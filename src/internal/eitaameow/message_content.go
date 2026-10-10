package eitaameow

import (
	"encoding/base64"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

func boundedText(s string, max int) bool { return utf8.ValidString(s) && len(s) <= max }

// structuredContent projects only reviewed message content. Provider hashes,
// raw vCards, opaque entities and nested transport data never enter event JSON.
func structuredContent(media object, msg *domains.Message) (object, error) {
	switch media.str("_") {
	case "messageMediaContact":
		phone, first, last := media.str("phone_number"), media.str("first_name"), media.str("last_name")
		if !boundedText(phone, 128) || !boundedText(first, 1024) || !boundedText(last, 1024) {
			return nil, protocolError()
		}
		contact := object{"phone_number": phone, "first_name": first, "last_name": last}
		if id := media.num("user_id"); id != 0 {
			contact["user_id"] = strconv.FormatInt(id, 10)
		}
		msg.Kind = "contact"
		msg.Supported = true
		return object{"contact": contact}, nil
	case "messageMediaGeo", "messageMediaGeoLive", "messageMediaVenue":
		geo := asObject(media["geo"])
		if geo.str("_") == "geoPointEmpty" {
			return nil, nil
		}
		if geo.str("_") != "geoPoint" && geo.str("_") != "geoPoint_84" {
			return nil, protocolError()
		}
		lat, latOK := geo["lat"].(float64)
		lon, lonOK := geo["long"].(float64)
		if !latOK || !lonOK || math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return nil, protocolError()
		}
		location := object{"latitude": lat, "longitude": lon}
		if media.str("_") == "messageMediaGeoLive" {
			if media.num("period") < 0 {
				return nil, protocolError()
			}
			location["live_period_seconds"] = media.num("period")
		}
		if media.str("_") == "messageMediaVenue" {
			title, address := media.str("title"), media.str("address")
			if !boundedText(title, 2048) || !boundedText(address, 4096) {
				return nil, protocolError()
			}
			location["title"] = title
			location["address"] = address
		}
		msg.Kind = "location"
		msg.Supported = true
		return object{"location": location}, nil
	case "messageMediaPoll":
		poll := asObject(media["poll"])
		if poll.str("_") != "poll" || poll.num("id") == 0 || !boundedText(poll.str("question"), 4096) {
			return nil, protocolError()
		}
		answers := asObjects(poll["answers"])
		if len(answers) < 2 || len(answers) > 10 {
			return nil, protocolError()
		}
		public := object{"id": strconv.FormatInt(poll.num("id"), 10), "question": poll.str("question")}
		for _, key := range []string{"closed", "multiple_choice", "public_voters", "quiz"} {
			value, _ := poll[key].(bool)
			public[key] = value
		}
		options := []object{}
		seen := map[string]bool{}
		for _, answer := range answers {
			option, ok := answer["option"].([]byte)
			if answer.str("_") != "pollAnswer" || !ok || len(option) < 1 || len(option) > 64 || !boundedText(answer.str("text"), 1024) {
				return nil, protocolError()
			}
			id := base64.StdEncoding.EncodeToString(option)
			if seen[id] {
				return nil, protocolError()
			}
			seen[id] = true
			options = append(options, object{"text": answer.str("text"), "option": id})
		}
		public["answers"] = options
		results := asObject(media["results"])
		if results.str("_") != "pollResults" {
			return nil, protocolError()
		}
		if _, ok := results["total_voters"]; ok {
			if results.num("total_voters") < 0 {
				return nil, protocolError()
			}
			public["total_voters"] = results.num("total_voters")
		}
		votes := asObjects(results["results"])
		if len(votes) > 10 {
			return nil, protocolError()
		}
		counts := []object{}
		for _, vote := range votes {
			option, ok := vote["option"].([]byte)
			id := base64.StdEncoding.EncodeToString(option)
			if vote.str("_") != "pollAnswerVoters" || !ok || !seen[id] || vote.num("voters") < 0 {
				return nil, protocolError()
			}
			chosen, _ := vote["chosen"].(bool)
			correct, _ := vote["correct"].(bool)
			counts = append(counts, object{"option": id, "voters": vote.num("voters"), "chosen": chosen, "correct": correct})
		}
		if len(counts) > 0 {
			public["results"] = counts
		}
		msg.Kind = "poll"
		msg.Supported = true
		return object{"poll": public}, nil
	}
	return nil, nil
}
