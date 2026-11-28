package retention

import "testing"

func TestParseDate(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"  ", "", false},
		{"2027-06-30", "2027-06-30", false},
		{" 2027-06-30 ", "2027-06-30", false},
		{"2028-02-29", "2028-02-29", false}, // a leap day
		{"1965-07-04", "1965-07-04", false}, // past dates are fine
		{"2027-02-29", "", true},            // not a leap year
		{"2027-02-30", "", true},
		{"2027-06-31", "", true},
		{"2027-13-01", "", true},
		{"2027-6-3", "", true},
		{"06/07/2027", "", true},
		{"01.06.2027", "", true},
		{"forever", "", true}, // file_date is a date only
		{"7y", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDate(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ParseDate(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseRetention(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"7y", "7y", false},
		{" 7Y ", "7y", false},
		{"18m", "18m", false},
		{"6w", "6w", false},
		{"30d", "30d", false},
		{"1000y", "1000y", false},
		{"07y", "7y", false}, // stored without leading zeros
		{"0030D", "30d", false},
		{"forever", "forever", false},
		{"Forever", "forever", false},
		{"2030-03-14", "2030-03-14", false},
		{"2015-01-01", "2015-01-01", false}, // in the past: the file is simply past its retention
		{"0y", "", true},
		{"1001y", "", true},
		{"+7y", "", true},
		{"7", "", true},
		{"7x", "", true},
		{"y", "y", false}, // a policy name: filo update asks before creating one, so a typo for 7y is caught there
		{"7 y", "", true},
		{"-1y", "", true},
		{"2027-02-29", "", true},
		{"14/03/2030", "", true},
		{"indefinitely", "indefinitely", false}, // a policy name (slice 2 of named retention policies)
		{"Bills", "bills", false},
		{" end-of-the-year ", "end-of-the-year", false},
		{"bills!", "", true},
		{"my bills", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRetention(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ParseRetention(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDateFromName(t *testing.T) {
	const today = "2026-11-11" // so the latest date read is 2026-12-11
	tests := map[string]string{
		"2023-03-14 invoice.pdf":                 "2023-03-14",
		"invoice_2023_03_14.pdf":                 "2023-03-14",
		"scan.2023.03.14.jpg":                    "2023-03-14",
		"IMG_20230714_1032.jpg":                  "2023-07-14",
		"2023-03 invoice.pdf":                    "2023-03-31", // the month's last day
		"2024-02 rent.pdf":                       "2024-02-29",
		"report-2023.03.pdf":                     "2023-03-31",
		"1965-07-04 beach.jpg":                   "1965-07-04",
		"2026-12-11 booking.pdf":                 "2026-12-11", // 30 days ahead: still read
		"2026-12-12 booking.pdf":                 "",           // further ahead: not a date
		"1245-06 charter.pdf":                    "",           // before 1900
		"1899-12-31.txt":                         "",
		"2099-01-01 plan.md":                     "",
		"2019 report.pdf":                        "", // a lone year is not read
		"03-04-2023 letter.pdf":                  "", // day and month cannot be told apart
		"14-03-2023.pdf":                         "",
		"2023-01-01 to 2023-12-31 statement.pdf": "",           // two dates: never guessed
		"2023-03-14 copy of 20230314.pdf":        "2023-03-14", // the same date twice
		"2023-02-30 typo.pdf":                    "",
		"2023-13 report.pdf":                     "",
		"2023-03-14-2.pdf":                       "2023-03-14",
		"2023-03_14.pdf":                         "", // mixed separators
		"12023-03-14.pdf":                        "", // part of a longer number
		"2023-03-145.pdf":                        "",
		"202303141.jpg":                          "",
		"20230314":                               "2023-03-14",
		"notes.md":                               "",
		"":                                       "",
	}
	r := NewDateReader(today)
	for name, want := range tests {
		if got := r.Read(name); got != want {
			t.Errorf("Read(%q) = %q, want %q", name, got, want)
		}
		if got := DateFromName(name, today); got != want {
			t.Errorf("DateFromName(%q) = %q, want %q", name, got, want)
		}
	}
	if got := NewDateReader("not a day").Read("2023-03-14.pdf"); got != "" {
		t.Errorf("a reader with no valid today read %q", got)
	}
}
