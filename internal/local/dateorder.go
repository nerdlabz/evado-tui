package local

import "regexp"

// monthFirstZones are the zones whose people write a short numeric date month
// first: the United States and its territories, Canada, the Philippines, and
// China, Taiwan, Japan and Korea, whose year-month-day order leaves 3/4 as
// March 4. It is the service's list (defaultDateOrder in src/lib/dates.ts);
// every other zone, UTC included, reads day first.
var monthFirstZones = regexp.MustCompile("^(?:" +
	"US/.+|Canada/.+|EST|MST|HST|EST5EDT|CST6CDT|MST7MDT|PST8PDT|Navajo" +
	"|America/(?:New_York|Detroit|Kentucky/.+|Louisville|Indiana/.+|Indianapolis|Fort_Wayne|Knox_IN|Chicago|Menominee" +
	"|North_Dakota/.+|Denver|Boise|Shiprock|Phoenix|Los_Angeles|Anchorage|Juneau|Sitka|Metlakatla|Yakutat|Nome|Adak|Atka" +
	"|Puerto_Rico|St_Thomas|Virgin" +
	"|St_Johns|Halifax|Glace_Bay|Moncton|Goose_Bay|Blanc-Sablon|Toronto|Montreal|Nipigon|Thunder_Bay|Iqaluit|Pangnirtung" +
	"|Atikokan|Coral_Harbour|Winnipeg|Rainy_River|Resolute|Rankin_Inlet|Regina|Swift_Current|Edmonton|Cambridge_Bay" +
	"|Yellowknife|Inuvik|Creston|Dawson_Creek|Fort_Nelson|Whitehorse|Dawson|Vancouver)" +
	"|Pacific/(?:Honolulu|Johnston|Guam|Saipan|Pago_Pago|Samoa|Midway|Wake|Majuro|Kwajalein|Pohnpei|Ponape|Chuuk|Truk|Yap|Kosrae|Palau)" +
	"|Asia/(?:Manila|Shanghai|Chongqing|Chungking|Harbin|Urumqi|Taipei|Tokyo|Seoul|Pyongyang)" +
	"|PRC|ROC|ROK|Japan" +
	")$")

// ValidDateOrder reports whether s is a date order a person can choose.
func ValidDateOrder(s string) bool { return s == "mdy" || s == "dmy" }

// DefaultDateOrder is the order a date written in numbers reads in when
// nobody has chosen one, from the time zone: what the service gives an
// account that has not chosen.
func DefaultDateOrder(tz string) string {
	if monthFirstZones.MatchString(tz) {
		return "mdy"
	}
	return "dmy"
}

// PreferDateOrder sets the order to read numeric dates in when the account
// has not given one, which is always the case without an account. An empty
// or unknown order goes back to following the time zone. It is for the
// caller that opens the backend to set once, before anything reads with it,
// so it takes no lock.
func (b *Backend) PreferDateOrder(order string) {
	if !ValidDateOrder(order) {
		order = ""
	}
	b.dateOrder = order
}

// readDateOrder is the order numeric dates read in here: the account's, else
// the one set with PreferDateOrder, else the time zone's. The caller holds
// the lock.
func (b *Backend) readDateOrder() string {
	if ValidDateOrder(b.st.User.DateOrder) {
		return b.st.User.DateOrder
	}
	if b.dateOrder != "" {
		return b.dateOrder
	}
	return DefaultDateOrder(b.st.User.TZ)
}
