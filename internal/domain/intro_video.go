package domain

import "time"

// IntroVideoSequence is created only after a successful opening. Its stable ID
// makes retries and repeated forms share one case video per contact.
func IntroVideoSequence(s State, openingID, mediaURL string, now time.Time) []Effect {
	if openingID == "" || !ValidHTTPS(mediaURL) || !s.Profile.Consent || s.OptedOut || s.Human || s.Paused {
		return nil
	}
	return []Effect{{ID: "fx/intro-video:" + s.ID, LeadID: s.ID, Kind: "send_video", Purpose: "intro_video", MediaURL: mediaURL, AfterMessageID: openingID, Due: now.Add(2 * time.Second), Expires: now.Add(15 * time.Minute)}}
}
