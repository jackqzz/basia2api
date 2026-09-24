package api

import (
	"net/http"

	siteadapter "bps-2api/internal/adapter/bps"
	"bps-2api/internal/secret"
)

// 账号池管理页（bps 版）：全量账号 + 邮箱/账号 ID/套餐（凭据加密存储，此处解密展示）
// + 就绪状态（TokenLocal）+ 测活/批量启停（复用 accounts/actions 任务）。

type poolAccountRow struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	AccountID  string `json:"account_id"`
	Plan       string `json:"plan"`
	HasRefresh bool   `json:"has_refresh"`
	Ready      bool   `json:"ready"`
	Failures   int    `json:"failures"`
	ExpiresAt  int64  `json:"expires_at"`
	Inflight   int    `json:"inflight"`
	Enabled    bool   `json:"enabled"`
}

// handleAdminPoolAccounts 返回账号池明细（含解密后的身份信息）。
func (s *Server) handleAdminPoolAccounts(w http.ResponseWriter, r *http.Request) {
	var failCounts map[string]int
	if s.ka != nil {
		failCounts = s.ka.FailureCounts()
	}
	rows := make([]poolAccountRow, 0, 64)
	for _, acc := range s.pool.Accounts() {
		snap := acc.Snapshot()
		row := poolAccountRow{
			Name:      snap.Name,
			Ready:     snap.LoggedIn,
			Failures:  failCounts[snap.Name],
			ExpiresAt: snap.ExpiresAt,
			Inflight:  snap.Inflight,
			Enabled:   !snap.Disabled,
		}
		if tok := acc.Tokens.Current(); tok != nil {
			row.HasRefresh = tok.RefreshToken != ""
			sec := siteadapter.ParseSecret(secret.Open(tok.RefreshToken))
			if sec.AccessToken == "" {
				sec = siteadapter.ParseSecret(tok.AccessToken)
			}
			row.Email = sec.Email
			row.AccountID = sec.AccountID
			row.Plan = sec.PlanType
			if row.Email == "" && tok.AccessToken != "" {
				row.Email = siteadapter.Email(tok.AccessToken)
			}
			if row.AccountID == "" && tok.AccessToken != "" {
				row.AccountID = siteadapter.AccountID(tok.AccessToken)
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": rows, "total": len(rows)})
}

// handleAdminPoolView 返回内置的账号池 HTML 页。
func (s *Server) handleAdminPoolView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(poolViewHTML))
}

const poolViewHTML = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8"><title>账号池</title>
<style>
body{font:14px/1.5 -apple-system,"PingFang SC",sans-serif;margin:20px;background:#0f1115;color:#e6e6e6}
.bar{margin-bottom:12px;display:flex;gap:10px;align-items:center;flex-wrap:wrap}
button{background:#2456c4;color:#fff;border:0;border-radius:6px;padding:6px 14px;cursor:pointer;font-size:13px}
button.gray{background:#3a4152}button:disabled{opacity:.4;cursor:default}
table{border-collapse:collapse;width:100%;background:#171a21;border-radius:8px;overflow:hidden}
td,th{padding:5px 10px;border-bottom:1px solid #22262f;font-size:13px;text-align:left;white-space:nowrap}
tr:hover{background:#1c2029}
.badge{padding:1px 8px;border-radius:10px;font-size:12px}
.ok{background:#1d4030;color:#5ce6a0}.no{background:#45202a;color:#ff8fa3}
.count{color:#8b93a7;font-size:13px}
</style></head><body>
<h2>账号池 <span class="count" id="sum"></span></h2>
<div class="bar">
 <button onclick="act('refresh_usage')">测活选中</button>
 <button class="gray" onclick="act('enable')">启用选中</button>
 <button class="gray" onclick="act('disable')">禁用选中</button>
 <span class="count">|</span>
 <label><input type="checkbox" id="onlyDead" onchange="render()"> 只看未就绪</label>
 <button class="gray" onclick="load()">刷新</button>
 <span id="msg" class="count"></span>
</div>
<table><thead><tr>
 <th><input type="checkbox" id="all" onchange="selAll()"></th><th>账号</th><th>邮箱</th><th>账号ID</th><th>套餐</th><th>可续期</th><th>状态</th><th>连败</th><th>token到期</th><th>在飞</th>
</tr></thead><tbody id="tb"></tbody></table>
<script>
var rows=[];
function pad(n){return n<10?'0'+n:n}
function exp(t){if(!t)return'—';var d=new Date(t*1000);return (d.getMonth()+1)+'-'+pad(d.getDate())+' '+pad(d.getHours())+':'+pad(d.getMinutes())}
async function load(){
 try{
  var d=await (await fetch('/api/admin/pool/accounts')).json();
  rows=d.accounts||[];
  sum.textContent='共 '+rows.length+' · 就绪 '+rows.filter(function(r){return r.ready}).length+' · 未就绪 '+rows.filter(function(r){return !r.ready}).length;
  render();
 }catch(e){msg.textContent='加载失败：'+e}
}
function render(){
 var only=onlyDead.checked;
 tb.innerHTML=rows.filter(function(r){return !only||!r.ready}).map(function(r,i){
  return '<tr><td><input type="checkbox" data-i="'+i+'" '+(r.ready?'':'checked')+'></td>'+
   '<td>'+r.name+'</td><td>'+(r.email||'—')+'</td>'+
   '<td>'+(r.account_id||'—')+'</td><td>'+(r.plan||'—')+'</td>'+
   '<td>'+(r.has_refresh?'是':'否')+'</td>'+
   '<td><span class="badge '+(r.ready?'ok':'no')+'">'+(r.ready?'就绪':'未就绪')+'</span>'+(r.enabled?'':' ⏸')+'</td>'+
   '<td>'+(r.failures>0?('<span class="badge no">'+r.failures+'</span>'):'0')+'</td>'+
   '<td>'+exp(r.expires_at)+'</td><td>'+r.inflight+'</td></tr>';
 }).join('');
}
function selAll(){var c=all.checked;tb.querySelectorAll('input').forEach(function(x){x.checked=c})}
function picked(){return Array.from(tb.querySelectorAll('input:checked')).map(function(x){return rows[+x.dataset.i].name})}
async function act(a){
 var names=picked();
 if(!names.length){msg.textContent='先勾选账号';return}
 msg.textContent='提交中…';
 try{
  var res=await fetch('/api/admin/accounts/actions',{method:'POST',headers:{'Content-Type':'application/json'},
   body:JSON.stringify({action:a,names:names})});
  var d=await res.json();
  if(!res.ok)throw new Error(d.error&&d.error.message||res.status);
  msg.textContent='已提交 '+names.length+' 个（'+a+'）';
  setTimeout(load,3000);
 }catch(e){msg.textContent='失败：'+e}
}
load();setInterval(load,30000);
</script></body></html>`
