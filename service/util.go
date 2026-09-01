package service

import "time"

// timeNowNano 返回当前时间的纳秒时间戳，用于密钥生成的兜底方案。
func timeNowNano() int64 { return time.Now().UnixNano() }
