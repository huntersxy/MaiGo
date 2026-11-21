package qq_official

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const uploadTimeout = 300 * time.Second

func (c *Client) uploadMedia(ctx context.Context, path string, part outboundPart) (string, error) {
	binary := part.binary
	prepare, err := c.apiPost(ctx, path+"/upload_prepare", map[string]any{
		"file_type": part.fileType, "file_size": strconv.Itoa(len(binary)), "file_name": part.name,
		"md5": fmt.Sprintf("%x", md5.Sum(binary)), "sha1": fmt.Sprintf("%x", sha1.Sum(binary)),
		"md5_10m": fmt.Sprintf("%x", md5.Sum(binary[:min(len(binary), 10002432)])),
	}, uploadTimeout)
	if err != nil {
		return "", err
	}
	id := asString(prepare["upload_id"])
	size, ok := asInt(prepare["block_size"])
	parts, _ := prepare["parts"].([]any)
	if id == "" || !ok || size <= 0 || len(parts) == 0 || len(binary) == 0 {
		return "", fmt.Errorf("QQ 媒体预上传响应缺少 upload_id、block_size 或 parts")
	}
	base := 1
	indexes := make(map[int]struct{}, len(parts))
	for _, raw := range parts {
		part, _ := asMap(raw)
		index, ok := asInt(part["index"])
		if !ok || index < 0 {
			return "", fmt.Errorf("QQ 媒体分片序号无效")
		}
		if _, duplicate := indexes[index]; duplicate {
			return "", fmt.Errorf("QQ 媒体分片序号重复")
		}
		indexes[index] = struct{}{}
		base = min(base, index)
	}
	if len(parts) != 1+(len(binary)-1)/size {
		return "", fmt.Errorf("QQ 媒体预上传返回的分片数量无效")
	}
	for _, raw := range parts {
		part, _ := asMap(raw)
		index, _ := asInt(part["index"])
		block := index - base
		if block < 0 || block > (len(binary)-1)/size {
			return "", fmt.Errorf("QQ 媒体分片 %d 超出文件范围", index)
		}
		offset := block * size
		chunk := binary[offset : offset+min(size, len(binary)-offset)]
		target, err := url.Parse(asString(part["presigned_url"]))
		if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil {
			return "", fmt.Errorf("QQ 媒体分片 %d 的预签名地址无效", index)
		}
		if err := c.putChunk(ctx, target.String(), chunk, index); err != nil {
			return "", err
		}
		if _, err := c.apiPost(ctx, path+"/upload_part_finish", map[string]any{
			"upload_id": id, "part_index": index, "block_size": strconv.Itoa(len(chunk)), "md5": fmt.Sprintf("%x", md5.Sum(chunk)),
		}, uploadTimeout); err != nil {
			return "", err
		}
	}
	result, err := c.apiPost(ctx, path+"/files", map[string]any{"file_type": part.fileType, "srv_send_msg": false, "file_name": part.name, "upload_id": id}, uploadTimeout)
	if err != nil {
		return "", err
	}
	info := asString(result["file_info"])
	if info == "" {
		return "", fmt.Errorf("QQ 媒体上传响应缺少 file_info")
	}
	return info, nil
}

func (c *Client) putChunk(ctx context.Context, target string, data []byte, index int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("QQ 媒体分片 %d 请求地址无效", index)
	}
	client := c.mediaHTTP()
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("QQ 媒体分片 %d 上传失败（%T）", index, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("QQ 媒体分片 %d 上传失败: HTTP %d", index, res.StatusCode)
	}
	return nil
}

func (c *Client) downloadFile(ctx context.Context, endpoint string, size int) ([]byte, error) {
	target, err := url.Parse(endpoint)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil {
		return nil, fmt.Errorf("宿主附件下载地址无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("宿主附件下载地址无效")
	}
	client := c.mediaHTTP()
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("宿主附件下载失败（%T）", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("宿主附件下载失败: HTTP %d", res.StatusCode)
	}
	if res.ContentLength >= 0 && res.ContentLength != int64(size) {
		return nil, fmt.Errorf("宿主附件下载大小与声明不一致")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, int64(size)+1))
	if err != nil || len(data) != size {
		return nil, fmt.Errorf("宿主附件下载不完整或超出声明大小")
	}
	return data, nil
}

func (c *Client) mediaHTTP() http.Client {
	client := *c.http
	client.Timeout = uploadTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}
