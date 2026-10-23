package offline

import (
	"time"
)

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

type Term struct {
	Kind     string `json:"kind"`
	Amount   int    `json:"amount,omitempty"`
	EndDate  string `json:"endDate,omitempty"`
	StartsAt string `json:"startsAt,omitempty"`
}
type Period struct {
	NotBefore string  `json:"notBefore"`
	ExpiresAt *string `json:"expiresAt"`
}
type TermResult struct {
	Anchor string `json:"anchor"`
	Period
}

// CalculateTerm is the only service-side term calculator. The browser submits
// intent; it never supplies a trusted calculated expiration.
func CalculateTerm(now time.Time, action string, term Term, current *Period) (TermResult, error) {
	now = now.UTC().Truncate(time.Second)
	start := now
	anchor := now
	if now.Year() < 2000 || now.Year() > 9999 {
		return TermResult{}, invalid("签发日期超出支持范围")
	}
	switch action {
	case "initial":
		if current != nil {
			return TermResult{}, invalid("首次签发不能包含原授权")
		}
		if term.StartsAt != "" {
			parsed, err := ParseUTC(term.StartsAt)
			if err != nil {
				return TermResult{}, err
			}
			if parsed.Before(now) || parsed.Year() < 2000 || parsed.Nanosecond() != 0 {
				return TermResult{}, invalid("生效时间必须是当前或未来的整秒")
			}
			start = parsed
			anchor = parsed
		}
	case "renew":
		if current == nil || current.ExpiresAt == nil {
			return TermResult{}, invalid("永久授权无需续期")
		}
		if term.StartsAt != "" {
			return TermResult{}, invalid("续期不能另设生效时间")
		}
		oldStart, err := ParseUTC(current.NotBefore)
		if err != nil {
			return TermResult{}, err
		}
		end, err := ParseUTC(*current.ExpiresAt)
		if err != nil {
			return TermResult{}, err
		}
		if oldStart.After(now) {
			start = oldStart
		}
		if end.After(anchor) {
			anchor = end
		}
	case "transfer":
		if current == nil {
			return TermResult{}, invalid("换机缺少原授权")
		}
		if _, err := ParseUTC(current.NotBefore); err != nil {
			return TermResult{}, err
		}
		if current.ExpiresAt != nil {
			end, err := ParseUTC(*current.ExpiresAt)
			if err != nil {
				return TermResult{}, err
			}
			if !now.Before(end) {
				return TermResult{}, &Error{"LICENSE_EXPIRED", "授权已到期，请先续期再换机"}
			}
		}
		return TermResult{now.Format(timestampLayout), *current}, nil
	default:
		return TermResult{}, invalid("不支持的签发操作")
	}
	var end *time.Time
	switch term.Kind {
	case "perpetual":
		if term.Amount != 0 || term.EndDate != "" {
			return TermResult{}, invalid("永久授权不能同时设置期限")
		}
	case "days":
		if term.Amount < 1 || term.Amount > 36500 || term.EndDate != "" {
			return TermResult{}, invalid("天数须为 1 至 36500 的整数")
		}
		v := anchor.Add(time.Duration(term.Amount) * 24 * time.Hour)
		end = &v
	case "months":
		if term.Amount < 1 || term.Amount > 1200 || term.EndDate != "" {
			return TermResult{}, invalid("月数须为 1 至 1200 的整数")
		}
		local := anchor.In(beijing)
		year, month, day := local.Date()
		target := time.Date(year, month+time.Month(term.Amount), 1, local.Hour(), local.Minute(), local.Second(), 0, beijing)
		last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, beijing).Day()
		if day > last {
			day = last
		}
		v := time.Date(target.Year(), target.Month(), day, local.Hour(), local.Minute(), local.Second(), 0, beijing).UTC()
		end = &v
	case "date":
		if term.Amount != 0 {
			return TermResult{}, invalid("指定截止日不能同时设置天数或月数")
		}
		date, err := time.ParseInLocation("2006-01-02", term.EndDate, beijing)
		if err != nil || date.Format("2006-01-02") != term.EndDate || date.Year() < 2000 {
			return TermResult{}, invalid("请选择有效的截止日期")
		}
		v := date.AddDate(0, 0, 1).UTC()
		end = &v
	default:
		return TermResult{}, invalid("请选择有效的授权期限")
	}
	result := TermResult{Anchor: anchor.Format(timestampLayout), Period: Period{NotBefore: start.Format(timestampLayout)}}
	if end != nil {
		if end.Year() > 9999 || !end.After(start) {
			return TermResult{}, invalid("截止时间必须晚于生效时间且不超出 9999 年")
		}
		if action == "renew" && current.ExpiresAt != nil {
			old, _ := ParseUTC(*current.ExpiresAt)
			if !end.After(old) {
				return TermResult{}, invalid("续期必须延长原截止时间")
			}
		}
		formatted := end.UTC().Format(timestampLayout)
		result.ExpiresAt = &formatted
	}
	return result, nil
}
func Usable(period Period, now time.Time) bool {
	start, err := ParseUTC(period.NotBefore)
	if err != nil || now.Before(start) {
		return false
	}
	if period.ExpiresAt == nil {
		return true
	}
	end, err := ParseUTC(*period.ExpiresAt)
	return err == nil && now.Before(end)
}
