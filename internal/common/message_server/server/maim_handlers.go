package server

import "net/http"

// MaimLegacyHandler 把旧版 maim_message 协议挂到宿主 IPC 上的 /_maim/ws
func (s *Server) MaimLegacyHandler() http.HandlerFunc {
	if s == nil {
		return nil
	}
	return s.handleWS
}

// MaimAPIHandler 把新 API Server 协议挂到宿主 IPC 上的 /_maim/api
func (s *Server) MaimAPIHandler() http.HandlerFunc {
	if s == nil {
		return nil
	}
	return s.handleAPIWS
}
