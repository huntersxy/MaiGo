package weixin_clawbot

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) downloadImage(ctx context.Context, item map[string]any) ([]byte, error) {
	media, _ := asMap(item["media"])
	query := asString(media["encrypt_query_param"])
	if query == "" {
		return nil, fmt.Errorf("微信图片缺少下载参数")
	}
	endpoint := DefaultCDNURL + "/download?" + url.Values{"encrypted_query_param": {query}}.Encode()
	response, err := c.cdnRequest(ctx, "download", http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(c.maxBytes)+aes.BlockSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > c.maxBytes+aes.BlockSize {
		return nil, fmt.Errorf("图片超过大小限制")
	}
	var key []byte
	if encoded := asString(item["aeskey"]); encoded != "" {
		key, err = hex.DecodeString(encoded)
	} else if encoded := asString(media["aes_key"]); encoded != "" {
		key, err = decodeMediaKey(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("微信图片密钥格式无效")
	}
	if key != nil {
		data, err = decryptImage(data, key)
		if err != nil {
			return nil, err
		}
	}
	if len(data) > c.maxBytes {
		return nil, fmt.Errorf("图片超过大小限制")
	}
	return data, nil
}

func decodeMediaKey(encoded string) ([]byte, error) {
	encoded += strings.Repeat("=", (4-len(encoded)%4)%4)
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil && len(key) == 32 {
		key, err = hex.DecodeString(string(key))
	}
	if err != nil || len(key) != aes.BlockSize {
		return nil, fmt.Errorf("微信图片密钥格式无效")
	}
	return key, nil
}

func decryptImage(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("微信图片密文格式无效")
	}
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		chunk := data[offset : offset+aes.BlockSize]
		block.Decrypt(chunk, chunk)
	}
	width := int(data[len(data)-1])
	if width < 1 || width > aes.BlockSize {
		return nil, fmt.Errorf("微信图片填充无效")
	}
	for _, b := range data[len(data)-width:] {
		if b != byte(width) {
			return nil, fmt.Errorf("微信图片填充无效")
		}
	}
	return data[:len(data)-width], nil
}

func (c *Client) uploadImage(ctx context.Context, user string, data []byte) (map[string]any, error) {
	if len(data) == 0 || len(data) > c.maxBytes {
		return nil, fmt.Errorf("图片为空或超过大小限制")
	}
	var key, id [16]byte
	rand.Read(key[:])
	rand.Read(id[:])
	fileKey := hex.EncodeToString(id[:])
	hexKey := hex.EncodeToString(key[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	width := aes.BlockSize - len(data)%aes.BlockSize
	encrypted := make([]byte, len(data)+width)
	copy(encrypted, data)
	for i := len(data); i < len(encrypted); i++ {
		encrypted[i] = byte(width)
	}
	for offset := 0; offset < len(encrypted); offset += aes.BlockSize {
		chunk := encrypted[offset : offset+aes.BlockSize]
		block.Encrypt(chunk, chunk)
	}
	result, err := c.Call(ctx, "getuploadurl", map[string]any{
		"filekey": fileKey, "media_type": 1, "to_user_id": user, "rawsize": len(data),
		"rawfilemd5": fmt.Sprintf("%x", md5.Sum(data)), "filesize": len(encrypted),
		"no_need_thumb": true, "aeskey": hexKey,
	}, nil, false)
	if err != nil {
		return nil, err
	}
	var endpoint string
	if fullURL := asString(result["upload_full_url"]); fullURL != "" {
		endpoint, err = trustedURL(fullURL)
		if err != nil {
			return nil, err
		}
	} else if param := asString(result["upload_param"]); param != "" {
		endpoint = DefaultCDNURL + "/upload?" + url.Values{"encrypted_query_param": {param}, "filekey": {fileKey}}.Encode()
	} else {
		return nil, fmt.Errorf("微信未返回图片上传地址")
	}
	response, err := c.cdnRequest(ctx, "upload", http.MethodPost, endpoint, encrypted)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	query := response.Header.Get("x-encrypted-param")
	if query == "" {
		return nil, fmt.Errorf("微信图片上传响应缺少下载参数")
	}
	return map[string]any{"type": 2, "image_item": map[string]any{
		"media":    map[string]any{"encrypt_query_param": query, "aes_key": base64.StdEncoding.EncodeToString([]byte(hexKey)), "encrypt_type": 1},
		"mid_size": len(encrypted),
	}}, nil
}

type mediaTransportError struct{ cause error }

func (e *mediaTransportError) Error() string {
	return fmt.Sprintf("微信媒体网络请求失败（%T）", e.cause)
}

func (e *mediaTransportError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.cause, &timeout) && timeout.Timeout()
}

func (e *mediaTransportError) Temporary() bool { return false }

func (c *Client) cdnRequest(ctx context.Context, action, method, endpoint string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("微信媒体请求地址无效")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, &mediaTransportError{cause: err}
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, &APIError{Code: response.StatusCode, Endpoint: "cdn/" + action}
	}
	return response, nil
}
