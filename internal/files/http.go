package filesvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

var bytesRangePattern = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// writeFileError 按 FastAPI HTTPException 的载荷返回 {"detail": ...}
func writeFileError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	payload, err := json.Marshal(map[string]string{"detail": message})
	if err != nil {
		return
	}
	_, _ = w.Write(payload)
}

func (s *Service) handleDownload(headOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		token := chi.URLParam(r, "token")
		claims, err := s.Claims(token, "download")
		if err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		if claims.Scope != "" {
			if _, err := s.Claims(claims.Scope, "plugin"); err != nil {
				writeFileError(w, http.StatusForbidden, err.Error())
				return
			}
		}
		asset, err := s.store.Get(claims.FileRef, "", true)
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		if asset.State != "ready" {
			writeFileError(w, http.StatusNotFound, "附件尚未就绪")
			return
		}

		start, end := int64(0), asset.Size-1
		rangeHeader := ""
		if !headOnly {
			rangeHeader = r.Header.Get("Range")
		}
		if rangeHeader != "" {
			start, end, err = parseByteRange(rangeHeader, asset.Size)
			if err != nil {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", asset.Size))
				writeFileError(w, http.StatusRequestedRangeNotSatisfiable, err.Error())
				return
			}
		}
		length := end - start + 1
		if headOnly {
			setDownloadHeaders(w, asset, start, end, length, rangeHeader)
			w.WriteHeader(statusForRange(rangeHeader))
			return
		}
		// Python 先占用读取名额并打开 reader，再写响应头；否则并发拒绝会带着
		// 文件的 Content-Length 返回 429，客户端会按错误长度解析
		if !s.AcquireRead() {
			writeFileError(w, http.StatusTooManyRequests, "文件读取并发数已满")
			return
		}
		file, err := s.store.OpenReader(asset)
		if err != nil {
			_ = s.ReleaseRead()
			writeFileHTTPError(w, err)
			return
		}
		defer func() {
			file.Close()
			_ = s.store.CloseReader(asset.FileRef)
			_ = s.ReleaseRead()
		}()
		setDownloadHeaders(w, asset, start, end, length, rangeHeader)
		if rangeHeader != "" {
			w.WriteHeader(http.StatusPartialContent)
		}
		writeTimeout := s.cfg.TransferTimeout
		if writeTimeout <= 0 {
			writeTimeout = 120 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
		defer cancel()
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(writeTimeout)); err == nil {
			defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		}
		if err := CopyBlobRangeContext(ctx, file, start, length, w); err != nil {
			return
		}
	}
}

// quoteRFC5987 复刻 Python urllib.parse.quote(value, safe=”)，用于 filename*
// 仅保留 ALPHA、DIGIT 与 _.-~，其余 UTF-8 字节按大写十六进制转义
func quoteRFC5987(value string) string {
	const upperHex = "0123456789ABCDEF"
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		ch := value[index]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '_' || ch == '.' || ch == '-' || ch == '~' {
			builder.WriteByte(ch)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(upperHex[ch>>4])
		builder.WriteByte(upperHex[ch&0x0f])
	}
	return builder.String()
}
func setDownloadHeaders(w http.ResponseWriter, asset Asset, start, end, length int64, rangeHeader string) {
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+quoteRFC5987(asset.Name))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", fmt.Sprintf("%q", asset.SHA256))
	if rangeHeader != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, asset.Size))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
}

func statusForRange(rangeHeader string) int {
	if rangeHeader != "" {
		return http.StatusPartialContent
	}
	return http.StatusOK
}

func parseByteRange(header string, size int64) (start, end int64, err error) {
	match := bytesRangePattern.FindStringSubmatch(header)
	if match == nil || size == 0 {
		return 0, 0, fmt.Errorf("无效字节范围")
	}
	left, right := match[1], match[2]
	if left == "" && right == "" {
		return 0, 0, fmt.Errorf("无效字节范围")
	}
	start = 0
	end = size - 1
	if left != "" {
		start, err = strconv.ParseInt(left, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("无效字节范围")
		}
		if right != "" {
			end, err = strconv.ParseInt(right, 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("无效字节范围")
			}
			if end >= size {
				end = size - 1
			}
		}
	} else {
		suffix, err := strconv.ParseInt(right, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, fmt.Errorf("无效字节范围")
		}
		start = size - suffix
		if start < 0 {
			start = 0
		}
	}
	if start > end {
		return 0, 0, fmt.Errorf("字节范围超出文件")
	}
	return start, end, nil
}

func writeFileHTTPError(w http.ResponseWriter, err error) {
	var remoteErr *remoteDownloadError
	var permissionErr *remotePermissionError
	switch {
	case errors.Is(err, ErrDisabled), errors.Is(err, ErrForbidden):
		writeFileError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrNotFound):
		writeFileError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNotReady):
		writeFileError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrLimit), errors.Is(err, ErrBusy):
		writeFileError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotCache), errors.Is(err, errRemoteInvalidURL), errors.Is(err, ErrInvalidRef), errors.Is(err, ErrInvalidName):
		writeFileError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		writeFileError(w, http.StatusGatewayTimeout, "文件传输超时")
	case errors.As(err, &permissionErr):
		writeFileError(w, http.StatusForbidden, err.Error())
	case errors.As(err, &remoteErr):
		writeFileError(w, http.StatusBadGateway, err.Error())
	default:
		writeFileError(w, http.StatusBadRequest, err.Error())
	}
}
