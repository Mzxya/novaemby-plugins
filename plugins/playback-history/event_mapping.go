package main

import "time"

// Current mapping confirmed by the 2026-11-12 raw playback samples.
// Keep media ID, playback session ID and device ID distinct; do not infer IDs
// from unrelated numbers or interpret unknown fields as completion flags.
func decodeObservedMapping(root map[string]any, at time.Time) (EventData, time.Time, bool) {
	session := object(root["IitkoSG"])
	if session == nil {
		return EventData{}, at, false
	}
	e := EventData{}
	e.SessionID = first(text(session["HLLdFN9"]), text(session["I2OacnY0_Q"]))
	e.UserID = text(session["UPwHQQxhi"])
	e.UserName = text(session["VsLsiqKa8fCX"])
	e.ItemID = first(text(session["F0Oqaz6KpkFL"]), text(session["MCU3o9oQz"]))
	e.ItemName = text(root["Nd3qBGBfh6"])
	e.ItemType = text(root["NP7awI"])
	e.DeviceName = text(session["UpksZ80r"])
	e.DeviceID = text(session["J8VWLzj"])
	e.Client = text(session["ESj6ExD"])
	e.PositionTicks = integer(session["BxlCZoT"])
	_, e.HasPosition = session["BxlCZoT"]
	e.RuntimeTicks = integer(session["AaBTFESMF"])
	e.Paused, e.HasPaused = session["YRFBd_"].(bool)
	// Preserve subsecond ordering only when the payload agrees with the SDK's
	// event timestamp. Session start/last-activity fields are not event time.
	precise, err := time.Parse(time.RFC3339Nano, text(root["BLq_NRPB"]))
	if err == nil && (at.IsZero() || precise.Truncate(time.Second).Equal(at.Truncate(time.Second))) {
		at = precise
	}
	return e, at, true
}
