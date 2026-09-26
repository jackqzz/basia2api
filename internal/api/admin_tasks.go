package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"bps-2api/internal/adminapi"
	"bps-2api/internal/pool"
	"bps-2api/internal/tasks"
)

func (s *Server) handleAdminTasks(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not ready"})
		return
	}
	list := s.jobs.Store().List(200)
	if list == nil {
		list = []tasks.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":       list,
		"total":       len(list),
		"concurrency": s.jobs.Concurrency(),
	})
}

func (s *Server) handleAdminTaskOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.jobs.Store().Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleAdminTaskCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	id := r.PathValue("id")
	if err := s.jobs.Cancel(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	t, _ := s.jobs.Store().Get(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
}

func (s *Server) handleAdminTaskEvents(w http.ResponseWriter, r *http.Request) {
	s.streamTaskEvents(w, r, r.PathValue("id"))
}

func (s *Server) handleAdminTaskEventsAll(w http.ResponseWriter, r *http.Request) {
	s.streamTaskEvents(w, r, "")
}

func (s *Server) streamTaskEvents(w http.ResponseWriter, r *http.Request, id string) {
	if s.jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not ready"})
		return
	}
	if id != "" {
		if _, ok := s.jobs.Store().Get(id); !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	ch, unsub := s.jobs.Store().Subscribe(id)
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if id != "" {
		if t, ok := s.jobs.Store().Get(id); ok {
			b, _ := json.Marshal(tasks.Event{Type: "status", Task: t})
			fmt.Fprintf(w, "event: status\ndata: %s\n\n", b)
			flusher.Flush()
		}
	} else {
		fmt.Fprintf(w, ": connected\n\n")
		flusher.Flush()
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			typ := ev.Type
			if typ == "" {
				typ = "log"
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
			flusher.Flush()
		}
	}
}

type accountBatchAction struct {
	Action  string   `json:"action"` // enable, disable, delete, refresh_usage, assign_proxy, cooldown_clear, login
	Names   []string `json:"names"`
	ProxyID *string  `json:"proxy_id"`
	// Method 只对 login 有意义：browser（默认）/ auto（协议优先，失败降级浏览器）。
	Method string `json:"method"`
}

func (s *Server) handleAdminAccountActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var body accountBatchAction
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json body"})
		return
	}
	body.Action = strings.TrimSpace(body.Action)
	if body.Action == "" || len(body.Names) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action and names are required"})
		return
	}
	names := append([]string(nil), body.Names...)
	if body.Action == "login" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "浏览器登录已移除：请导入 ChatGPT 凭据（access_token / refresh_token），token 过期会自动续期",
		})
		return
	}
	t := s.jobs.Enqueue(tasks.CreateRequest{
		Type:       "accounts." + body.Action,
		Title:      accountActionTitle(body.Action, len(names)),
		Total:      len(names),
		Cancelable: true,
		Meta:       map[string]any{"action": body.Action, "names": names},
		Run: func(ctx *tasks.Context) (any, error) {
			return s.runAccountBatch(ctx, body.Action, names, body.ProxyID)
		},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"task": t})
}

func accountActionTitle(action string, n int) string {
	switch action {
	case "enable":
		return fmt.Sprintf("批量启用 %d 个账号", n)
	case "disable":
		return fmt.Sprintf("批量停用 %d 个账号", n)
	case "delete":
		return fmt.Sprintf("批量删除 %d 个账号", n)
	case "refresh_usage":
		return fmt.Sprintf("刷新 %d 个账号用量", n)
	case "assign_proxy":
		return fmt.Sprintf("为 %d 个账号分配出口", n)
	case "cooldown_clear":
		return fmt.Sprintf("清除 %d 个账号冷却", n)
	default:
		return fmt.Sprintf("批量操作 %d 个账号", n)
	}
}

func (s *Server) runAccountBatch(ctx *tasks.Context, action string, names []string, proxyID *string) (any, error) {
	type item struct {
		Name  string `json:"name"`
		Error string `json:"error,omitempty"`
		OK    bool   `json:"ok"`
	}
	out := make([]item, len(names))
	sem := make(chan struct{}, s.jobs.Concurrency())
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN, failN := 0, 0
	for i, name := range names {
		if err := ctx.Err(); err != nil {
			return map[string]any{"items": out, "ok": okN, "failed": failN}, err
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			err := s.applyAccountAction(name, action, proxyID)
			mu.Lock()
			if err != nil {
				out[i] = item{Name: name, Error: err.Error()}
				failN++
				ctx.Error(name + ": " + err.Error())
			} else {
				out[i] = item{Name: name, OK: true}
				okN++
				ctx.Info(name + " 完成")
			}
			ctx.Progress(okN+failN, len(names), "")
			mu.Unlock()
		}(i, name)
	}
	wg.Wait()
	result := map[string]any{"items": out, "ok": okN, "failed": failN}
	if failN > 0 && okN == 0 {
		return result, fmt.Errorf("%d 个账号失败", failN)
	}
	return result, nil
}

func (s *Server) applyAccountAction(name, action string, proxyID *string) error {
	acc := s.pool.Get(name)
	if acc == nil && action != "delete" {
		return fmt.Errorf("account not found")
	}
	switch action {
	case "enable":
		on := true
		acc.ApplyAdminPatch(pool.AdminPatch{Enabled: &on})
		s.pool.RefreshAccountEgress(acc)
	case "disable":
		off := false
		acc.ApplyAdminPatch(pool.AdminPatch{Enabled: &off})
		s.pool.RefreshAccountEgress(acc)
	case "delete":
		return s.pool.Remove(name)
	case "refresh_usage":
		acc.RefreshUsage(s.websiteURL())
	case "assign_proxy":
		acc.ApplyAdminPatch(pool.AdminPatch{ProxyID: proxyID})
		s.pool.RefreshAccountEgress(acc)
	case "cooldown_clear":
		acc.ClearCooldown()
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	return nil
}

func (s *Server) handleAdminImportTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.jobs == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not ready"})
		return
	}
	var body struct {
		Accounts  json.RawMessage `json:"accounts"`
		Text      string          `json:"text"`
		Overwrite bool            `json:"overwrite"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json body"})
		return
	}
	var parsed []adminapi.AccountImport
	var err error
	// CSV（邮箱/密码/2FA）优先按站点侧解析：内核解析器会把表头当 token 收下。
	if looksLikeCredentialCSV(body.Text) {
		if imports, ok := credentialCSVToImports(body.Text, "acct"); ok {
			parsed = imports
		}
	}
	if parsed == nil {
		parsed, err = adminapi.ParseImportRequest(body.Accounts, body.Text)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	accounts := append([]adminapi.AccountImport(nil), parsed...)
	overwrite := body.Overwrite
	h := &adminapi.Handler{Pool: s.pool, AfterAccountChange: s.pool.RefreshAccountEgress}
	t := s.jobs.Enqueue(tasks.CreateRequest{
		Type:       "accounts.import",
		Title:      fmt.Sprintf("导入 %d 个账号", len(accounts)),
		Total:      len(accounts),
		Cancelable: true,
		Meta:       map[string]any{"overwrite": overwrite},
		Run: func(ctx *tasks.Context) (any, error) {
			return s.runAccountImport(ctx, h, accounts, overwrite)
		},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"task": t})
}

func (s *Server) runAccountImport(ctx *tasks.Context, h *adminapi.Handler, accounts []adminapi.AccountImport, overwrite bool) (any, error) {
	type item struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	out := make([]item, len(accounts))
	sem := make(chan struct{}, s.jobs.Concurrency())
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN, skipN, failN := 0, 0, 0
	for i, in := range accounts {
		if err := ctx.Err(); err != nil {
			return map[string]any{"items": out, "imported": okN, "skipped": skipN, "failed": failN}, err
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, in adminapi.AccountImport) {
			defer wg.Done()
			defer func() { <-sem }()
			imported, skipped, err := h.ImportOne(in, overwrite)
			name := strings.TrimSpace(in.Name)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				out[i] = item{Name: name, Status: "error", Error: err.Error()}
				failN++
				ctx.Error(name + ": " + err.Error())
			case skipped:
				out[i] = item{Name: name, Status: "skipped"}
				skipN++
				ctx.Info(name + " 已存在或身份重复，跳过")
			case imported:
				out[i] = item{Name: name, Status: "imported"}
				okN++
				ctx.Info(name + " 已导入")
			}
			ctx.Progress(okN+skipN+failN, len(accounts), "")
		}(i, in)
	}
	wg.Wait()
	result := map[string]any{"items": out, "imported": okN, "skipped": skipN, "failed": failN}
	if failN > 0 && okN == 0 {
		return result, fmt.Errorf("%d 条导入失败", failN)
	}
	return result, nil
}

func (s *Server) handleAdminUsageTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	accs := s.pool.Accounts()
	names := make([]string, 0, len(accs))
	for _, a := range accs {
		if a != nil {
			names = append(names, a.Name)
		}
	}
	t := s.jobs.Enqueue(tasks.CreateRequest{
		Type:       "usage.refresh_all",
		Title:      fmt.Sprintf("刷新全部账号用量（%d）", len(names)),
		Total:      len(names),
		Cancelable: true,
		Run: func(ctx *tasks.Context) (any, error) {
			return s.runAccountBatch(ctx, "refresh_usage", names, nil)
		},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"task": t})
}

func (s *Server) handleAdminModelsTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	t := s.jobs.Enqueue(tasks.CreateRequest{
		Type:       "models.refresh",
		Title:      "刷新模型目录",
		Total:      1,
		Cancelable: false,
		Run: func(ctx *tasks.Context) (any, error) {
			ctx.Info("正在向上游拉取模型列表")
			infos, err := s.catalog.Refresh()
			if err != nil {
				return nil, err
			}
			ctx.Progress(1, 1, fmt.Sprintf("共 %d 个模型", len(infos)))
			return map[string]any{"total": len(infos)}, nil
		},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"task": t})
}
