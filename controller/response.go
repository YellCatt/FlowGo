// Package controller 定义 HTTP 请求处理器，负责解析请求、调用 Service 层并返回 JSON。
package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/example/flowgo/logger"

	"go.uber.org/zap"
)

// envelope 统一的 JSON 响应包装结构。
type envelope struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// writeJSON 写入 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Error("响应编码失败，客户端可能无法收到完整数据", zap.Error(err))
	}
}

// writeOK 返回 200 及数据体。
func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, data)
}

// writeCreated 返回 201 及数据体。
func writeCreated(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusCreated, data)
}

// writeError 按错误映射状态码并返回错误描述。
func writeError(w http.ResponseWriter, status int, err error) {
	logger.Debug("返回错误响应",
		zap.Int("状态码", status),
		zap.String("错误信息", err.Error()),
	)
	writeJSON(w, status, envelope{Error: err.Error()})
}

// writeBadRequest 返回 400 错误。
func writeBadRequest(w http.ResponseWriter, err error) { writeError(w, http.StatusBadRequest, err) }

// writeNotFound 返回 404 错误。
func writeNotFound(w http.ResponseWriter, err error) { writeError(w, http.StatusNotFound, err) }

// writeInternal 返回 500 错误，并记录到错误日志便于排查。
func writeInternal(w http.ResponseWriter, err error) {
	logger.Error("服务器内部错误", zap.Error(err))
	writeError(w, http.StatusInternalServerError, err)
}

// decodeJSON 解析请求体为指定结构，非法 JSON 返回 400。
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errBadJSON
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return errBadJSON
	}
	return nil
}

// readRunPayload 提取触发负载，作为模板变量 trigger 的来源。
// 优先使用 JSON 请求体，其次表单与查询参数，纯文本则包装为 {"raw": "..."}。
func readRunPayload(r *http.Request) string {
	if err := r.ParseForm(); err != nil {
		logger.Debug("请求表单解析失败，已忽略表单参数", zap.Error(err))
	}

	if form := mergeValues(r.PostForm); len(form) > 0 {
		if raw, err := json.Marshal(form); err == nil {
			return string(raw)
		}
	}

	if query := mergeValues(r.URL.Query()); len(query) > 0 {
		if raw, err := json.Marshal(query); err == nil {
			return string(raw)
		}
	}

	if r.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		logger.Warn("读取请求体失败，将使用空触发参数", zap.Error(err))
		return ""
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return ""
	}
	if json.Valid(raw) {
		return string(raw)
	}
	wrapped, _ := json.Marshal(map[string]string{"raw": string(raw)})
	return string(wrapped)
}

// mergeValues 将多值参数合并为单值映射，重复键取第一个值。
func mergeValues(v map[string][]string) map[string]string {
	if len(v) == 0 {
		return nil
	}
	out := make(map[string]string, len(v))
	for k, values := range v {
		if len(values) > 0 {
			out[k] = values[0]
		}
	}
	return out
}

// writeLogError 记录内部错误但不改变已发出的响应。
func writeLogError(err error) {
	logger.Error("重新加载定时任务失败", zap.Error(err))
}
