package main

import (
	"fmt"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

const rawLogChunkRunes = 1200
const rawLogMaxRunes = 128 * 1024

// Log before parsing, including unrecognized event structures. Segment boundaries
// use runes so Chinese text is never split inside a UTF-8 character.
func logRawEvent(event sdk.Event, sequence uint64, emit func(string, ...any)) {
	runes := []rune(string(event.Data))
	truncated := len(runes) > rawLogMaxRunes
	if truncated {
		runes = runes[:rawLogMaxRunes]
	}
	parts := max(1, (len(runes)+rawLogChunkRunes-1)/rawLogChunkRunes)
	identifier := fmt.Sprintf("%d-%d", time.Now().UnixNano(), sequence)
	emit("原始接收事件", "event", event.Name, "diagnosticId", identifier,
		"eventAt", event.At.Format(time.RFC3339Nano), "bytes", len(event.Data),
		"parts", parts, "truncated", truncated)
	for part := 0; part < parts; part++ {
		start := part * rawLogChunkRunes
		end := min(start+rawLogChunkRunes, len(runes))
		emit("原始事件数据", "event", event.Name, "diagnosticId", identifier,
			"part", fmt.Sprintf("%d/%d", part+1, parts), "raw", string(runes[start:end]))
	}
}
