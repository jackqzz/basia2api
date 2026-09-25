package api

import (
	"net/http"
)

// M1 指标出口：JSON 供程序化读取；实时容量面板已并入 React 控制台（/admin/metrics），
// /view 保留为兼容旧链接的重定向。

func (s *Server) handleAdminMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meters.Snapshot())
}

func (s *Server) handleAdminMetricsView(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/metrics", http.StatusFound)
}
