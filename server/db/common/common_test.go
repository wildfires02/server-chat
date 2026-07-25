package common

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"chat/server/store/types"
)

func genTestData() []types.Subscription {
	var testData = []types.Subscription{
		{ObjHeader: types.ObjHeader{Id: "1", UpdatedAt: time.Date(2021, time.June, 1, 1, 11, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "2", UpdatedAt: time.Date(2021, time.June, 2, 2, 12, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "3", UpdatedAt: time.Date(2021, time.June, 3, 3, 13, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "4", UpdatedAt: time.Date(2021, time.June, 4, 4, 14, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "5", UpdatedAt: time.Date(2021, time.June, 5, 5, 15, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "6", UpdatedAt: time.Date(2021, time.June, 6, 6, 16, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "7", UpdatedAt: time.Date(2021, time.June, 7, 7, 17, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "8", UpdatedAt: time.Date(2021, time.June, 8, 8, 18, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "9", UpdatedAt: time.Date(2021, time.June, 9, 9, 19, 0, 0, time.Local)}},
		{ObjHeader: types.ObjHeader{Id: "10", UpdatedAt: time.Date(2021, time.June, 10, 10, 20, 0, 0, time.Local)}},
	}

	testData[0].SetTouchedAt(time.Date(2021, time.June, 1, 1, 11, 0, 0, time.Local))   // 1
	testData[1].SetTouchedAt(time.Date(2021, time.June, 4, 4, 12, 0, 0, time.Local))   // 3
	testData[2].SetTouchedAt(time.Date(2021, time.June, 4, 2, 13, 0, 0, time.Local))   // 2
	testData[3].SetTouchedAt(time.Date(2021, time.June, 4, 4, 14, 0, 0, time.Local))   // 4
	testData[4].SetTouchedAt(time.Date(2021, time.June, 7, 5, 15, 0, 0, time.Local))   // 6
	testData[5].SetTouchedAt(time.Date(2021, time.June, 6, 6, 16, 0, 0, time.Local))   // 5
	testData[6].SetTouchedAt(time.Date(2021, time.June, 7, 7, 17, 0, 0, time.Local))   // 7
	testData[7].SetTouchedAt(time.Date(2021, time.June, 9, 8, 18, 0, 0, time.Local))   // 8
	testData[8].SetTouchedAt(time.Date(2021, time.June, 10, 11, 19, 0, 0, time.Local)) // 10
	testData[9].SetTouchedAt(time.Date(2021, time.June, 10, 10, 20, 0, 0, time.Local)) // 9

	return testData
}

func TestSelectEarliestUpdatedSubs(t *testing.T) {
	getOrder := func(subs []types.Subscription) string {
		var order []string
		for i := range subs {
			order = append(order, subs[i].Id)
		}
		return strings.Join(order, ",")
	}

	ims1 := time.Date(2021, time.June, 7, 8, 16, 15, 0, time.Local)
	ims2 := time.Date(2021, time.June, 4, 4, 13, 15, 0, time.Local)

	tests := []struct {
		name          string
		opts          *types.QueryOpt
		limit         int
		expectedOrder string
	}{
		{
			name:          "Return all unsorted",
			opts:          nil,
			limit:         100,
			expectedOrder: "1,2,3,4,5,6,7,8,9,10",
		},
		{
			name:          "Limit 9 earliest",
			opts:          nil,
			limit:         9,
			expectedOrder: "1,3,2,4,6,5,7,8,10",
		},
		{
			name:          "Limit in QueryOpt 20 and count 9",
			opts:          &types.QueryOpt{Limit: 20},
			limit:         9,
			expectedOrder: "1,3,2,4,6,5,7,8,10",
		},
		{
			name:          "Limit in QueryOpt 9 and count 20",
			opts:          &types.QueryOpt{Limit: 9},
			limit:         20,
			expectedOrder: "1,3,2,4,6,5,7,8,10",
		},
		{
			name:          "IfModifiedSince ims1",
			opts:          &types.QueryOpt{Limit: 6, IfModifiedSince: &ims1},
			limit:         20,
			expectedOrder: "8,10,9",
		},
		{
			name:          "IfModifiedSince ims2",
			opts:          &types.QueryOpt{Limit: 3, IfModifiedSince: &ims2},
			limit:         20,
			expectedOrder: "4,6,5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subs := SelectEarliestUpdatedSubs(genTestData(), tt.opts, tt.limit)
			sortOrder := getOrder(subs)
			if sortOrder != tt.expectedOrder {
				t.Errorf("Expected order '%s', got '%s'", tt.expectedOrder, sortOrder)
			}
		})
	}
}

func TestSelectLatestTime(t *testing.T) {
	t1 := time.Date(2021, time.June, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2021, time.June, 2, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		a, b     time.Time
		expected time.Time
	}{
		{name: "t1 before t2", a: t1, b: t2, expected: t2},
		{name: "t2 after t1", a: t2, b: t1, expected: t2},
		{name: "equal times", a: t1, b: t1, expected: t1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := SelectLatestTime(tt.a, tt.b)
			if !res.Equal(tt.expected) {
				t.Errorf("Expected %v, got %v", tt.expected, res)
			}
		})
	}
}

func TestRangesToSql(t *testing.T) {
	tests := []struct {
		name         string
		ranges       []types.Range
		expectedSql  string
		expectedArgs []any
	}{
		{
			name:         "Single range Hi=0",
			ranges:       []types.Range{{Low: 5, Hi: 0}},
			expectedSql:  "IN (?)",
			expectedArgs: []any{5},
		},
		{
			name:         "Single range Hi>0",
			ranges:       []types.Range{{Low: 5, Hi: 8}},
			expectedSql:  "BETWEEN ? AND ?",
			expectedArgs: []any{5, 7},
		},
		{
			name:         "Multiple ranges",
			ranges:       []types.Range{{Low: 1, Hi: 3}, {Low: 5, Hi: 0}, {Low: 8, Hi: 10}},
			expectedSql:  "IN (?,?,?,?,?)",
			expectedArgs: []any{1, 2, 5, 8, 9},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := RangesToSql(tt.ranges)
			if sql != tt.expectedSql {
				t.Errorf("Expected SQL '%s', got '%s'", tt.expectedSql, sql)
			}
			if !reflect.DeepEqual(args, tt.expectedArgs) {
				t.Errorf("Expected args %v, got %v", tt.expectedArgs, args)
			}
		})
	}
}

func TestDisjunctionSql(t *testing.T) {
	tests := []struct {
		name         string
		req          [][]string
		field        string
		expectedSql  string
		expectedArgs []any
	}{
		{
			name:         "Single disjunction",
			req:          [][]string{{"tag1", "tag2", "tag3"}},
			field:        "tagname",
			expectedSql:  "HAVING COUNT(tagname IN (?,?,?) OR NULL)>=1 ",
			expectedArgs: []any{"tag1", "tag2", "tag3"},
		},
		{
			name:         "Multiple disjunctions",
			req:          [][]string{{"tag1", "tag2"}, {"tag3"}, {"tag4", "tag5"}},
			field:        "fieldname",
			expectedSql:  "HAVING COUNT(fieldname IN (?,?) OR NULL)>=1 AND COUNT(fieldname IN (?) OR NULL)>=1 AND COUNT(fieldname IN (?,?) OR NULL)>=1 ",
			expectedArgs: []any{"tag1", "tag2", "tag3", "tag4", "tag5"},
		},
		{
			name:         "Disjunction with empty set",
			req:          [][]string{{"tag1"}, {}, {"tag2"}},
			field:        "fieldname",
			expectedSql:  "HAVING COUNT(fieldname IN (?) OR NULL)>=1 AND COUNT(fieldname IN (?) OR NULL)>=1 ",
			expectedArgs: []any{"tag1", "tag2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := DisjunctionSql(tt.req, tt.field)
			if sql != tt.expectedSql {
				t.Errorf("Expected SQL '%s', got '%s'", tt.expectedSql, sql)
			}
			if !reflect.DeepEqual(args, tt.expectedArgs) {
				t.Errorf("Expected args %v, got %v", tt.expectedArgs, args)
			}
		})
	}
}

func TestFilterFoundTags(t *testing.T) {
	setTags := types.StringSlice{"tag1", "tag2", "tag3", "tag4", "tag5"}
	index := map[string]struct{}{
		"tag1": {},
		"tag3": {},
		"tag5": {},
		"tag6": {},
	}

	tests := []struct {
		name     string
		setTags  types.StringSlice
		index    map[string]struct{}
		expected []string
	}{
		{
			name:     "Normal filter",
			setTags:  setTags,
			index:    index,
			expected: []string{"tag1", "tag3", "tag5"},
		},
		{
			name:     "Empty index",
			setTags:  setTags,
			index:    map[string]struct{}{},
			expected: []string{},
		},
		{
			name:     "Empty setTags",
			setTags:  types.StringSlice{},
			index:    index,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := FilterFoundTags(tt.setTags, tt.index)
			if !reflect.DeepEqual(res, tt.expected) {
				t.Errorf("Expected %v, got %v", tt.expected, res)
			}
		})
	}
}

func TestToJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		checkRes func(t *testing.T, res []byte)
	}{
		{
			name:  "Nil input",
			input: nil,
			checkRes: func(t *testing.T, res []byte) {
				if res != nil {
					t.Errorf("Expected nil, got %v", res)
				}
			},
		},
		{
			name:  "String input",
			input: "test string",
			checkRes: func(t *testing.T, res []byte) {
				expected := []byte(`"test string"`)
				if !reflect.DeepEqual(res, expected) {
					t.Errorf("Expected %v, got %v", expected, res)
				}
			},
		},
		{
			name:  "Map input",
			input: map[string]any{"key": "value", "number": 42},
			checkRes: func(t *testing.T, res []byte) {
				var parsed map[string]any
				if err := json.Unmarshal(res, &parsed); err != nil {
					t.Errorf("Failed to unmarshal result: %v", err)
				}
				if parsed["key"] != "value" || parsed["number"] != float64(42) {
					t.Errorf("JSON conversion failed, got %v", parsed)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ToJSON(tt.input)
			tt.checkRes(t, res)
		})
	}
}

func TestFromJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		checkRes func(t *testing.T, res any)
	}{
		{
			name:  "Nil input",
			input: nil,
			checkRes: func(t *testing.T, res any) {
				if res != nil {
					t.Errorf("Expected nil, got %v", res)
				}
			},
		},
		{
			name:  "Valid JSON bytes",
			input: []byte(`{"key": "value", "number": 42}`),
			checkRes: func(t *testing.T, res any) {
				if resultMap, ok := res.(map[string]any); ok {
					if resultMap["key"] != "value" || resultMap["number"] != float64(42) {
						t.Errorf("JSON deserialization failed, got %v", resultMap)
					}
				} else {
					t.Errorf("Expected map[string]any, got %T", res)
				}
			},
		},
		{
			name:  "Invalid JSON bytes",
			input: []byte(`{invalid json}`),
			checkRes: func(t *testing.T, res any) {
				if res != nil {
					t.Errorf("Expected nil for invalid JSON, got %v", res)
				}
			},
		},
		{
			name:  "Non-byte input",
			input: "not bytes",
			checkRes: func(t *testing.T, res any) {
				if res != nil {
					t.Errorf("Expected nil for non-byte input, got %v", res)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := FromJSON(tt.input)
			tt.checkRes(t, res)
		})
	}
}

func TestUpdateByMap(t *testing.T) {
	update := map[string]any{
		"Name":      "John Doe",
		"Age":       30,
		"Public":    map[string]string{"avatar": "url"},
		"Private":   map[string]string{"email": "john@example.com"},
		"Trusted":   map[string]bool{"verified": true},
		"UpdatedAt": time.Now(),
	}

	tests := []struct {
		name   string
		update map[string]any
	}{
		{name: "Standard Update Map", update: update},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cols, args := UpdateByMap(tt.update)
			if len(cols) != len(args) || len(cols) != len(tt.update) {
				t.Errorf("Expected %d columns and args, got %d cols and %d args", len(tt.update), len(cols), len(args))
			}

			for _, col := range cols {
				if !strings.Contains(col, "=?") {
					t.Errorf("Column should contain '=?', got %s", col)
				}
			}

			foundPublic, foundPrivate, foundTrusted := false, false, false
			for i, col := range cols {
				if strings.HasPrefix(col, "public=?") {
					foundPublic = true
					if _, ok := args[i].([]byte); !ok {
						t.Errorf("Public field should be []byte, got %T", args[i])
					}
				}
				if strings.HasPrefix(col, "private=?") {
					foundPrivate = true
					if _, ok := args[i].([]byte); !ok {
						t.Errorf("Private field should be []byte, got %T", args[i])
					}
				}
				if strings.HasPrefix(col, "trusted=?") {
					foundTrusted = true
					if _, ok := args[i].([]byte); !ok {
						t.Errorf("Trusted field should be []byte, got %T", args[i])
					}
				}
			}

			if !foundPublic || !foundPrivate || !foundTrusted {
				t.Error("Missing JSON fields in output")
			}
		})
	}
}

func TestExtractTags(t *testing.T) {
	tests := []struct {
		name     string
		update   map[string]any
		expected []string
	}{
		{
			name: "With Tags slice",
			update: map[string]any{
				"Name": "John",
				"Tags": types.StringSlice{"tag1", "tag2", "tag3"},
				"Age":  30,
			},
			expected: []string{"tag1", "tag2", "tag3"},
		},
		{
			name: "Without Tags field",
			update: map[string]any{
				"Name": "John",
				"Age":  30,
			},
			expected: nil,
		},
		{
			name: "With nil Tags field",
			update: map[string]any{
				"Name": "John",
				"Tags": nil,
				"Age":  30,
			},
			expected: nil,
		},
		{
			name: "With invalid Tags type",
			update: map[string]any{
				"Name": "John",
				"Tags": "not a slice",
				"Age":  30,
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := ExtractTags(tt.update)
			if !reflect.DeepEqual(tags, tt.expected) {
				t.Errorf("Expected %+v, got %+v", tt.expected, tags)
			}
		})
	}
}
