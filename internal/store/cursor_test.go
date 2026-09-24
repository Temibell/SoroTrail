package store

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeCompositeCursorRoundTrip(t *testing.T) {
	cases := []struct{ sortValue, id string }{
		{"123", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"2024-01-02T03:04:05.000000000Z", "00aabbccdd002233aabbccdd002233aabbccdd002233aabbccdd002233"},
		{"token#1", "c"},
	}
	for _, c := range cases {
		enc := encodeCompositeCursor(c.sortValue, c.id)
		sv, id, err := decodeCompositeCursor(enc)
		if err != nil {
			t.Fatalf("decode(%q): %v", enc, err)
		}
		if sv != c.sortValue || id != c.id {
			t.Fatalf("round trip = (%q,%q), want (%q,%q)", sv, id, c.sortValue, c.id)
		}
	}
}

func TestDecodeCompositeCursorRejectsInvalid(t *testing.T) {
	for _, c := range []string{"!!!not-base64!!!", "abcdef", "|", "lead|", "|tail"} {
		if _, _, err := decodeCompositeCursor(c); err == nil {
			t.Fatalf("decode(%q) expected error", c)
		} else if !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("decode(%q) error = %v, want ErrInvalidCursor", c, err)
		}
	}
}

func TestEncodeDecodeContractsCursor(t *testing.T) {
	enc := EncodeContractsCursor("value", "99", "CA000000000000000000000000000000000000000000000000000000")
	sv, id, err := DecodeContractsCursor(enc)
	if err != nil {
		t.Fatalf("DecodeContractsCursor: %v", err)
	}
	if sv != "99" || id != "CA000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("DecodeContractsCursor = (%q,%q)", sv, id)
	}
	if _, _, err := DecodeContractsCursor("!!!not-base64!!!"); err == nil || !errors.Is(err, ErrInvalidContractsCursor) {
		t.Fatalf("expected ErrInvalidContractsCursor, got %v", err)
	}
}

func TestEncodeCursorOrderByID(t *testing.T) {
	if got := EncodeCursor(OrderByID, Event{ID: "abc123"}); got != "abc123" {
		t.Fatalf("EncodeCursor(OrderByID) = %q, want abc123", got)
	}
}

func TestEncodeCursorCompositeOrdersRoundTrip(t *testing.T) {
	e := Event{ID: "abc123", Ledger: 99, CreatedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}
	for _, ob := range []string{OrderByLedger, OrderByCreatedAt} {
		cursor := EncodeCursor(ob, e)
		sv, id, err := decodeCompositeCursor(cursor)
		if err != nil {
			t.Fatalf("decode %s cursor: %v", ob, err)
		}
		if id != "abc123" {
			t.Fatalf("id = %q, want abc123", id)
		}
		if ob == OrderByLedger && sv != "99" {
			t.Fatalf("sort = %q, want 99", sv)
		}
	}
}

// Table test for the composite cursor encoder (issue #770). The cursor is
// the API's pagination contract: it must round-trip exactly, stay opaque,
// and be unambiguous about component order.
func TestEncodeCompositeCursor(t *testing.T) {
	cases := []struct {
		name      string
		sortValue string
		id        string
	}{
		{
			name:      "ledger sort value with a TOID id",
			sortValue: "123",
			id:        "0000000123-0000000001",
		},
		{
			name:      "timestamp sort value",
			sortValue: "2024-01-02T03:04:05.000000000Z",
			id:        "0000000456-0000000002",
		},
		{
			name:      "sort value containing the separator character stays decodable",
			sortValue: "a|b",
			id:        "c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := encodeCompositeCursor(tc.sortValue, tc.id)
			assert.NotEmpty(t, enc, "encoded cursor must not be empty")

			// Ordering preserved: the pair round-trips to the same components.
			sv, id, err := decodeCompositeCursor(enc)
			require.NoError(t, err)
			assert.Equal(t, tc.sortValue, sv, "sort value must round-trip")
			assert.Equal(t, tc.id, id, "id must round-trip")

			// Opaque: no readable ordering data leaks into the encoded form.
			if tc.sortValue != "" {
				assert.NotContains(t, enc, tc.sortValue)
			}
			if tc.id != "" {
				assert.NotContains(t, enc, tc.id)
			}
			assert.NotContains(t, enc, "|")
		})
	}

	t.Run("component ordering is preserved, not swapped", func(t *testing.T) {
		// Distinct components so a swap would not pass silently.
		sortValue, id := "777", "id-value"
		raw, err := base64.RawURLEncoding.DecodeString(encodeCompositeCursor(sortValue, id))
		require.NoError(t, err)
		sep := strings.LastIndex(string(raw), "|")
		require.GreaterOrEqual(t, sep, 0)
		assert.Equal(t, sortValue, string(raw[:sep]), "sort value must come before the separator")
		assert.Equal(t, id, string(raw[sep+1:]), "id must come after the separator")
	})

	t.Run("cursor encoded under one ordering is rejected under another", func(t *testing.T) {
		// OrderByID cursors are the raw event id, never a composite. Fed to
		// the composite decoder (the ledger/created_at path) they must be
		// rejected with the typed error the handler maps to 400, not
		// silently interpreted as a position.
		_, _, err := decodeCompositeCursor("0000000123-0000000001")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidCursor)
	})

	t.Run("zero position encodes to the documented empty cursor", func(t *testing.T) {
		// The zero Event's id is "", and OrderByID uses the id alone, so the
		// zero position encodes to "" — the documented empty cursor that
		// means "no cursor" (start of data / end of pages).
		assert.Empty(t, EncodeCursor(OrderByID, Event{}))
	})

	t.Run("empty components are rejected on decode", func(t *testing.T) {
		// The encoder can carry an empty sort value, but decoding it must
		// surface a typed error the handler maps to 400, never a valid
		// position — a component-less cursor has no defined ordering.
		for _, tc := range []struct{ sortValue, id string }{
			{"", "some-id"},
			{"123", ""},
			{"", ""},
		} {
			_, _, err := decodeCompositeCursor(encodeCompositeCursor(tc.sortValue, tc.id))
			require.Error(t, err, "cursor %q/%q must not decode", tc.sortValue, tc.id)
			assert.ErrorIs(t, err, ErrInvalidCursor)
		}
	})
}
