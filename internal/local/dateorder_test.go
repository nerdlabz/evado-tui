package local

import (
	"context"
	"testing"
	"time"
)

func TestDefaultDateOrder(t *testing.T) {
	for _, c := range []struct{ tz, want string }{
		{"America/New_York", "mdy"},
		{"America/Los_Angeles", "mdy"},
		{"America/Indiana/Knox", "mdy"},
		{"America/Toronto", "mdy"},
		{"US/Pacific", "mdy"},
		{"Pacific/Honolulu", "mdy"},
		{"Asia/Manila", "mdy"},
		{"Asia/Shanghai", "mdy"},
		{"Asia/Taipei", "mdy"},
		{"Asia/Tokyo", "mdy"},
		{"Asia/Seoul", "mdy"},
		{"Japan", "mdy"},
		{"Europe/Madrid", "dmy"},
		{"Europe/London", "dmy"},
		{"America/Sao_Paulo", "dmy"},
		{"America/Mexico_City", "dmy"},
		{"Asia/Kolkata", "dmy"},
		{"Australia/Sydney", "dmy"},
		{"UTC", "dmy"},
		{"Local", "dmy"},
		{"", "dmy"},
		{"Mars/Olympus_Mons", "dmy"},
		{"xAmerica/New_York", "dmy"},
		{"America/New_Yorkx", "dmy"},
	} {
		if got := DefaultDateOrder(c.tz); got != c.want {
			t.Errorf("DefaultDateOrder(%q) = %q; want %q", c.tz, got, c.want)
		}
	}
}

func readFive6(t *testing.T, b *Backend) string {
	t.Helper()
	loc := b.loc()
	b.now = func() time.Time { return time.Date(2031, 3, 3, 16, 0, 0, 0, loc) }
	p, err := b.Client().Parse(context.Background(), "Dentist 5/6")
	if err != nil || p.DoAt == nil {
		t.Fatalf("Parse 5/6 = %+v, %v", p, err)
	}
	return p.DoAt.In(loc).Format("2006-01-02")
}

// Without an account, numeric dates read in the order the computer's time
// zone gives, and the configured order beats it. An account's own choice
// beats both.
func TestCaptureReadsNumericDatesWithoutAnAccount(t *testing.T) {
	for _, c := range []struct {
		name, tz, account, prefer, want string
	}{
		{"US zone", "America/New_York", "", "", "2031-05-06"},
		{"European zone", "Europe/Madrid", "", "", "2031-06-05"},
		{"Asian month-first zone", "Asia/Tokyo", "", "", "2031-05-06"},
		{"unknown zone", "Mars/Olympus_Mons", "", "", "2031-06-05"},
		{"override day first in a US zone", "America/New_York", "", "dmy", "2031-06-05"},
		{"override month first in a European zone", "Europe/Madrid", "", "mdy", "2031-05-06"},
		{"invalid override follows the zone", "Europe/Madrid", "", "ymd", "2031-06-05"},
		{"account beats the override", "Europe/Madrid", "mdy", "dmy", "2031-05-06"},
		{"account beats the zone", "America/New_York", "dmy", "", "2031-06-05"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := open(t, "", "")
			b.st.User.TZ = c.tz
			b.st.User.DateOrder = c.account
			b.PreferDateOrder(c.prefer)
			if got := readFive6(t, b); got != c.want {
				t.Errorf("5/6 = %s; want %s", got, c.want)
			}
		})
	}
}
