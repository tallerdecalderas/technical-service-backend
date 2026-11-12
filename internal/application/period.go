package application

import (
	"strings"
	"time"
)

var artZone = time.FixedZone("ART", -3*60*60)

const periodDateLayout = "2006-01-02"

func parsePeriodDate(value string) (time.Time, bool) {
	parsed, err := time.ParseInLocation(periodDateLayout, strings.TrimSpace(value), artZone)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func periodDateString(value time.Time) string {
	return value.In(artZone).Format(periodDateLayout)
}

func todayART() time.Time {
	now := time.Now().In(artZone)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, artZone)
}
