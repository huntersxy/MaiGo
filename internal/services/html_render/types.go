package htmlrender

// Request 对齐 HtmlRenderRequest
type Request struct {
	HTML              string
	Selector          string
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
	FullPage          bool
	OmitBackground    bool
	WaitUntil         string
	WaitForSelector   string
	WaitForTimeoutMs  int
	TimeoutMs         int
	AllowNetwork      bool
}

// Result 对齐 HtmlRenderResult
type Result struct {
	ImageBase64 string
	MimeType    string
	Width       int
	Height      int
	RenderMs    int
}

// Payload 对齐能力层 to_payload
func (r *Result) Payload() map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"image_base64": r.ImageBase64,
		"mime_type":    r.MimeType,
		"width":        r.Width,
		"height":       r.Height,
		"render_ms":    r.RenderMs,
	}
}
