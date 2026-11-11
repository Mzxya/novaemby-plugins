package main

import "time"

// These are explicit mappings confirmed by raw playback samples, not heuristics.
// In particular, device ID, playback session ID, item ID and ticks are distinct.
type eventMapping struct {
	session, playback, deviceSession, userID, userName string
	itemString, itemNumber, itemName, itemType string
	deviceName, deviceID, client, position, runtime, paused, eventTime string
}

var observedMappings = []eventMapping{
	{
		session: "YINxkXn5Ll", playback: "FgxoGjZ9_14", deviceSession: "GgfWyZ",
		userID: "FGSkDNowvo", userName: "P4PkCnUa2L6",
		itemString: "IWlDpq61mjY", itemNumber: "TmILCY_xYpOi", itemName: "UVpRGwHrlK8b", itemType: "RxW1ToZ",
		deviceName: "Hy_WzZ2_qqP", deviceID: "TqM7bfKdgv", client: "A8MwMHz",
		position: "E1Aqki", runtime: "PX5tWDaaxa", paused: "GEsBLGVxh", eventTime: "W9e0V8XV5z2e",
	},
	{
		session: "UQVNVII2", playback: "Sk2P5N", deviceSession: "Lp_fY7Tvs7",
		userID: "EUebnB", userName: "FQBAcs2Ewxg",
		itemString: "V3S9zRa", itemNumber: "P0aX1v", itemName: "VE9xvSKkbW4R", itemType: "XaNoaNs",
		deviceName: "VapVfGw8id", deviceID: "Ma1NLTTGgLkj", client: "ZgySkQw",
		position: "AEKpVZufJ", runtime: "Hq1fULgr1q", paused: "VUWeWNJXBmau", eventTime: "IEbFReOhlBe",
	},
}

func decodeObservedMapping(root map[string]any, at time.Time) (EventData, time.Time, bool) {
	for _, mapping := range observedMappings {
		session := object(root[mapping.session])
		if session == nil { continue }
		e := EventData{}
		e.SessionID = first(text(session[mapping.playback]), text(session[mapping.deviceSession]))
		e.UserID = text(session[mapping.userID]); e.UserName = text(session[mapping.userName])
		e.ItemID = first(text(session[mapping.itemString]), text(session[mapping.itemNumber]))
		e.ItemName = text(root[mapping.itemName]); e.ItemType = text(root[mapping.itemType])
		e.DeviceName = text(session[mapping.deviceName]); e.DeviceID = text(session[mapping.deviceID]); e.Client = text(session[mapping.client])
		e.PositionTicks = integer(session[mapping.position]); _, e.HasPosition = session[mapping.position]
		e.RuntimeTicks = integer(session[mapping.runtime])
		e.Paused, e.HasPaused = session[mapping.paused].(bool)
		// The SDK envelope only carries Unix seconds. Keep the known event-time
		// field's subsecond precision when it agrees with that envelope.
		precise, err := time.Parse(time.RFC3339Nano, text(root[mapping.eventTime]))
		if err == nil && (at.IsZero() || precise.Truncate(time.Second).Equal(at.Truncate(time.Second))) { at = precise }
		// Other booleans in observed stopped events have no confirmed meaning.
		// Completion remains determined by the configured position/runtime rule.
		return e, at, true
	}
	return EventData{}, at, false
}
