package reconcile

import (
	"reflect"
	"testing"
)

func TestPair(t *testing.T) {
	s := func(id int, hash, name string) side { return side{ID: id, Hash: hash, Size: 10, Name: name} }
	tests := []struct {
		name       string
		olds, news []side
		want       [][2]int
	}{
		{"rename, one each side", []side{s(1, "h", "a")}, []side{s(2, "h", "b")}, [][2]int{{1, 2}}},
		{"different content never pairs", []side{s(1, "h", "a")}, []side{s(2, "x", "a")}, nil},
		{"two and two, different names: nothing",
			[]side{s(1, "h", "a"), s(2, "h", "b")}, []side{s(3, "h", "c"), s(4, "h", "d")}, nil},
		{"two and two, names unique on both sides: paired by name",
			[]side{s(1, "h", "a"), s(2, "h", "b")}, []side{s(3, "h", "b"), s(4, "h", "a")}, [][2]int{{2, 3}, {1, 4}}},
		{"one gone, two copies, one with its name",
			[]side{s(1, "h", "a")}, []side{s(2, "h", "a"), s(3, "h", "b")}, [][2]int{{1, 2}}},
		{"one gone, two copies, neither with its name",
			[]side{s(1, "h", "a")}, []side{s(2, "h", "b"), s(3, "h", "c")}, nil},
		{"one gone, two copies both with its name",
			[]side{s(1, "h", "a")}, []side{s(2, "h", "a"), s(3, "h", "a")}, nil},
		{"empty files never pair",
			[]side{{ID: 1, Hash: "e", Size: 0, Name: "a"}}, []side{{ID: 2, Hash: "e", Size: 0, Name: "a"}}, nil},
		{"no hash never pairs",
			[]side{{ID: 1, Size: 5, Name: "a"}}, []side{{ID: 2, Size: 5, Name: "a"}}, nil},
		{"two hash groups are independent",
			[]side{s(1, "h", "a"), s(2, "k", "b")}, []side{s(3, "k", "z"), s(4, "h", "y")}, [][2]int{{2, 3}, {1, 4}}},
		{"gone this scan and gone earlier, one new: name decides",
			[]side{s(1, "h", "a"), s(100, "h", "b")}, []side{s(2, "h", "b")}, [][2]int{{100, 2}}},
		{"gone this scan and gone earlier, one new, no name match: nothing",
			[]side{s(1, "h", "a"), s(100, "h", "b")}, []side{s(2, "h", "c")}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pair(tc.olds, tc.news)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pair = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPossibleMoves(t *testing.T) {
	s := func(id int, name string) side { return side{ID: id, Name: name} }
	tests := []struct {
		name       string
		olds, news []side
		want       [][2]int
	}{
		{"same name", []side{s(1, "a")}, []side{s(2, "a")}, [][2]int{{1, 2}}},
		{"different names", []side{s(1, "a")}, []side{s(2, "b")}, nil},
		{"name twice on the new side", []side{s(1, "a")}, []side{s(2, "a"), s(3, "a")}, nil},
		{"name twice on the old side", []side{s(1, "a"), s(4, "a")}, []side{s(2, "a")}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := possibleMoves(tc.olds, tc.news)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("possibleMoves = %v, want %v", got, tc.want)
			}
		})
	}
}
