package model

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// TimeLayout 全项目统一使用的时间格式化布局（东八区）。
const TimeLayout = "2006-01-02 15:04:05"

var localZone = time.FixedZone("CST", 8*60*60) // 东八区

// Time 以本地时区序列化时间的包装类型，避免 JSON 输出 UTC 造成阅读困惑。
type Time struct {
	time.Time
}

// Now 返回当前时间的 Time 包装值。
func Now() Time { return Time{Time: time.Now()} }

// MarshalJSON 按 TimeLayout 输出本地时间字符串。
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.Time.In(localZone).Format(TimeLayout) + `"`), nil
}

// UnmarshalJSON 兼容 RFC3339 与 TimeLayout 两种输入格式。
func (t *Time) UnmarshalJSON(data []byte) error {
	s := string(data)
	if s == "null" || s == `""` {
		t.Time = time.Time{}
		return nil
	}
	s = s[1 : len(s)-1]
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, TimeLayout} {
		if v, err := time.ParseInLocation(layout, s, localZone); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("unsupported time format: %s", s)
}

// Value 实现 driver.Valuer，入库时写入本地时间。
func (t Time) Value() (driver.Value, error) {
	if t.Time.IsZero() {
		return nil, nil
	}
	return t.Time.In(localZone), nil
}

// Scan 实现 sql.Scanner，从数据库读回时间。
func (t *Time) Scan(v any) error {
	switch val := v.(type) {
	case nil:
		t.Time = time.Time{}
	case time.Time:
		t.Time = val
	default:
		return fmt.Errorf("unsupported scan type for Time: %T", v)
	}
	return nil
}
