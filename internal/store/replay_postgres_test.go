package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Table test for the replay projection helper (issue #769). A wrong
// projection replays the wrong rows.
func TestEventIDs(t *testing.T) {
	cases := []struct {
		name   string
		events []EventDecoding
		want   []string
	}{
		{
			name: "populated batch yields ids in the same order",
			events: []EventDecoding{
				{ID: "evt-1", Topics: json.RawMessage(`["a"]`), Value: json.RawMessage(`{"v":1}`)},
				{ID: "evt-2", Topics: json.RawMessage(`["b"]`), Value: json.RawMessage(`{"v":2}`)},
				{ID: "evt-3"},
			},
			want: []string{"evt-1", "evt-2", "evt-3"},
		},
		{
			name:   "empty batch yields an empty slice rather than nil",
			events: []EventDecoding{},
			want:   []string{},
		},
		{
			name:   "nil batch yields an empty slice rather than nil",
			events: nil,
			want:   []string{},
		},
		{
			name: "duplicate ids are preserved positionally",
			// The batch feeds UPDATE ... FROM (SELECT unnest($1::text[])) ...
			// with parallel unnest arrays, so ids must stay index-aligned with
			// the topics/values arrays — de-duplicating here would corrupt the
			// row-wise pairing and replay the wrong rows.
			events: []EventDecoding{
				{ID: "evt-1"},
				{ID: "evt-1"},
				{ID: "evt-2"},
			},
			want: []string{"evt-1", "evt-1", "evt-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eventIDs(tc.events)
			assert.Equal(t, tc.want, got)
			if tc.events == nil {
				// make([]string, 0) is non-nil even for a nil input.
				assert.NotNil(t, got)
			}
		})
	}
}

func TestEventTopics(t *testing.T) {
	events := []EventDecoding{
		{ID: "a", Topics: json.RawMessage(`["x"]`)},
		{ID: "b", Topics: json.RawMessage(`["y","z"]`)},
	}
	got := eventTopics(events)
	if len(got) != 2 || string(got[0]) != `["x"]` || string(got[1]) != `["y","z"]` {
		t.Fatalf("eventTopics() = %q, want [[\"x\"] [\"y\",\"z\"]]", got)
	}
	if got := len(eventTopics(nil)); got != 0 {
		t.Fatalf("eventTopics(nil) len = %d, want 0", got)
	}
}

func TestEventValues(t *testing.T) {
	events := []EventDecoding{
		{ID: "a", Value: json.RawMessage(`{"v":1}`)},
		{ID: "b", Value: json.RawMessage(`{"v":2}`)},
	}
	got := eventValues(events)
	if len(got) != 2 || string(got[0]) != `{"v":1}` || string(got[1]) != `{"v":2}` {
		t.Fatalf("eventValues() = %q, want [{\"v\":1} {\"v\":2}]", got)
	}
	if got := len(eventValues(nil)); got != 0 {
		t.Fatalf("eventValues(nil) len = %d, want 0", got)
	}
}
