package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"

	"forest/go-api/internal/newapibridge"
)

func newAPIOAuthQuery(q url.Values) (newapibridge.OAuthRequest, error) {
	for key, value := range q {
		if len(value) != 1 {
			return newapibridge.OAuthRequest{}, newapibridge.ErrInvalid
		}
		switch key {
		case "client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "code_challenge", "code_challenge_method":
		default:
			return newapibridge.OAuthRequest{}, newapibridge.ErrInvalid
		}
	}
	return newapibridge.OAuthRequest{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), ResponseType: q.Get("response_type"), Scope: q.Get("scope"), State: q.Get("state"), Nonce: q.Get("nonce"), CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: q.Get("code_challenge_method")}, nil
}

func renderNewAPIPortal(w http.ResponseWriter, r *http.Request, svc newAPIBridgeService) {
	newAPIHeaders(w)
	if !newAPIRequireMethod(w, r, http.MethodGet) {
		return
	}
	if svc == nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	info, err := svc.PublicInfo(r.Context())
	if err != nil {
		newAPIError(w, err)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 4096 {
		newAPIError(w, newapibridge.ErrInvalid)
		return
	}
	var oauth any
	if r.URL.Path == "/new-api/authorize" {
		req, e := newAPIOAuthQuery(q)
		if e != nil {
			newAPIError(w, e)
			return
		}
		if e = svc.ValidateOAuth(r.Context(), req); e != nil {
			newAPIError(w, e)
			return
		}
		oauth = req
	} else if len(q) > 1 || len(q) > 0 && len(q["checkout"]) != 1 {
		newAPIError(w, newapibridge.ErrInvalid)
		return
	}
	nonceBytes := make([]byte, 24)
	if _, err = rand.Read(nonceBytes); err != nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	nonce := base64.RawStdEncoding.EncodeToString(nonceBytes)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	_ = newAPIPortalTemplate.Execute(w, map[string]any{"Nonce": nonce, "Bootstrap": map[string]any{"info": info, "oauth": oauth, "checkout": q.Get("checkout")}})
}

var newAPIPortalTemplate = template.Must(template.New("newapi-portal").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><title>AI 服务 · 本站余额</title>
<style>
:root{color-scheme:light dark;font:15px/1.65 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f4f6fa;color:#17233c}*{box-sizing:border-box}body{margin:0;padding:28px 16px 50px}main{max-width:760px;margin:auto}.brand{font-size:13px;color:#637189;margin:12px 0}h1{font-size:28px;line-height:1.3;margin:8px 0 16px}h2{font-size:18px;margin:0 0 14px}.card{background:#fff;border:1px solid #e5e9f1;border-radius:16px;padding:24px;margin:16px 0;box-shadow:0 3px 15px #20334d05}.muted{color:#68778e;font-size:13px}label{display:block;margin:14px 0 6px}input{width:100%;padding:12px;border:1px solid #ccd5e4;border-radius:9px;background:transparent;color:inherit;font:inherit}button,a.button{border:0;border-radius:9px;padding:11px 18px;display:inline-block;text-decoration:none;background:#265fea;color:white;font:inherit;cursor:pointer}button.secondary{background:#edf1f8;color:#34455f}button:disabled{opacity:.5;cursor:wait}.row{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.amount{font-size:34px;font-weight:650;letter-spacing:-1px}.notice{padding:12px 16px;border-radius:10px;background:#fff5df;color:#805219;margin:14px 0;overflow-wrap:anywhere}.error{background:#ffeded;color:#9b2332}.success{background:#eaf9f1;color:#176746}[hidden]{display:none!important}.table{overflow-x:auto}table{border-collapse:collapse;width:100%;font-size:13px}th,td{padding:11px 9px;border-bottom:1px solid #e5e9f1;text-align:left;white-space:nowrap}small{font-size:12px}#identity{overflow-wrap:anywhere}#login-form{max-width:430px}.detail{overflow-wrap:anywhere}.footer{text-align:center;color:#7b879a;font-size:12px;margin-top:25px}@media(max-width:480px){body{padding:16px 12px 30px}.card{padding:18px}.amount{font-size:30px}h1{font-size:24px}.actions button,.actions a{flex:1;text-align:center;white-space:nowrap}}@media(prefers-color-scheme:dark){:root{background:#111723;color:#e6ecf6}.card{background:#1b2433;border-color:#303c50}input{border-color:#45536c}.muted,.brand{color:#a4afc2}button.secondary{background:#303d54;color:#e6ecf6}th,td{border-color:#303c50}.notice{background:#372d1e;color:#f0d39f}.error{background:#40252a;color:#ffb2ba}.success{background:#18392e;color:#a9e7c9}}
</style></head><body><main>
<div class="brand">FOREST · AI 服务</div><h1 id="title">本站余额与 AI 服务</h1><p class="muted">V2B 余额单向转入 AI 服务，和套餐、佣金分别记账。</p>
<div id="message" role="status" class="notice" hidden></div>
<section id="disabled" class="card" hidden><h2>接入尚未启用</h2><p>管理员需要先配置本站地址、统一登录和余额支付信息。现在不会扣除任何余额。</p></section>
<section id="login" class="card" hidden><h2>使用本站账号继续</h2><p class="muted">使用原 V2B 邮箱和密码，无需注册新的本站账号。密码仅提交给当前本站，不发送到 AI 站。</p><form id="login-form"><label for="email">邮箱</label><input id="email" type="email" autocomplete="username" required maxlength="254"><label for="password">密码</label><input id="password" type="password" autocomplete="current-password" required maxlength="1024"><div class="actions"><button id="login-button" type="submit">登录本站</button></div></form></section>
<section id="account" class="card" hidden><div class="row"><div><div class="muted" id="identity"></div><div class="muted">本站可用余额</div><div class="amount" id="balance"></div></div><button id="logout" class="secondary">退出</button></div><div id="authorize-area" hidden><p>同意向 AI 服务提供本站账号标识、邮箱及显示名称，用于创建或登录关联账号；不会共享密码、套餐或订阅凭据。</p><button id="authorize">同意并登录 AI 服务</button></div><div id="home-actions" class="actions"><a class="button" id="open-ai" rel="noreferrer">进入 AI 服务</a><button class="secondary" id="reload">刷新记录</button></div><p class="muted" id="guide">首次进入 AI 站时选择“Forest / 本站账号”登录。在 AI 站发起充值，选择“本站余额”，会回到这里确认扣款。</p></section>
<section id="checkout" class="card" hidden><h2>确认划转到 AI 服务</h2><div class="amount" id="pay-amount"></div><p class="detail muted" id="pay-detail"></p><p id="pay-status"></p><div class="actions"><button id="pay">确认扣除本站余额</button><button class="secondary" id="refresh-pay">核对到账状态</button><a class="button" id="back-ai" rel="noreferrer" hidden>返回 AI 服务</a></div><p class="muted">确认后只扣普通余额，不扣佣金。通知暂时失败时会自动核对同一订单，不重复扣款；到账未确认前不会自动退款。</p></section>
<section id="history" class="card" hidden><div class="row"><h2>划转记录</h2><span class="muted" id="page-label"></span></div><div class="table"><table><thead><tr><th>订单</th><th>金额</th><th>状态</th><th>时间</th></tr></thead><tbody id="records"></tbody></table></div><div class="actions"><button class="secondary" id="prev">上一页</button><button class="secondary" id="next">下一页</button></div></section>
<div class="footer">本站余额 → AI 额度 · 仅本人账号可确认</div></main>
<script nonce="{{.Nonce}}">
'use strict';
const bootstrap={{.Bootstrap}};
const $=id=>document.getElementById(id), money=fen=>'¥'+(Number(fen)/100).toFixed(2);
const storageKey='forest_newapi_scoped_session';let token='',page=1,poll=null;
try{token=sessionStorage.getItem(storageKey)||''}catch{}
function saveToken(value){token=value;try{if(value)sessionStorage.setItem(storageKey,value);else sessionStorage.removeItem(storageKey)}catch{}}
function notice(text,type=''){const el=$('message');el.textContent=text;el.className='notice '+type;el.hidden=!text}
function expired(){saveToken('');$('login').hidden=false;$('account').hidden=true;$('history').hidden=true;$('checkout').hidden=true;if(poll)clearTimeout(poll)}
async function request(path,body,authenticated=true){const headers={};if(body!==undefined)headers['Content-Type']='application/json';if(authenticated&&token)headers.Authorization='Bearer '+token;const response=await fetch(path,{method:body===undefined?'GET':'POST',headers,body:body===undefined?undefined:JSON.stringify(body),cache:'no-store',credentials:'omit'});let data;try{data=await response.json()}catch{throw Error('服务器响应异常，请稍后核对交易记录')};if(!response.ok){if(response.status===401||(response.status===403&&path.endsWith('/info')))expired();throw Error(data.message||'请求未完成，请核对后再试')}return data.data}
async function busy(id,fn){const b=$(id);b.disabled=true;try{await fn()}catch(e){notice(e.message,'error')}finally{b.disabled=false}}
async function loadHistory(){const data=await request('/api/v1/user/new-api/transfers?current='+page+'&page_size=10');const tbody=$('records');tbody.replaceChildren();for(const item of data.items||[]){const tr=document.createElement('tr');const values=[item.remote_trade_no,money(item.amount_fen),item.status==='succeeded'?'已到账':item.last_error?'待核对，自动处理中':'处理中',new Date(item.created_at*1000).toLocaleString()];for(const value of values){const td=document.createElement('td');td.textContent=value;tr.appendChild(td)}tbody.appendChild(tr)}$('prev').disabled=page<=1;$('next').disabled=page*10>=data.total;$('page-label').textContent='第 '+page+' 页 · 共 '+data.total+' 笔';$('history').hidden=false}
async function loadCheckout(){if(!bootstrap.checkout)return;const data=await request('/api/v1/user/new-api/checkout?id='+encodeURIComponent(bootstrap.checkout));$('checkout').hidden=false;$('pay-amount').textContent=money(data.amount_fen);$('pay-detail').textContent='AI 账号 #'+data.remote_user_id+' · 订单 '+data.remote_trade_no;$('pay-status').textContent=data.status==='succeeded'?'已到账，无需再次支付':data.status==='pending'?'本站已扣款，正在核对 AI 站到账情况':data.status==='expired'?'该付款确认已过期，请重新发起充值':'将从当前本站账户余额扣除以上金额';$('pay').hidden=data.status!=='awaiting_confirmation';$('back-ai').hidden=data.status!=='succeeded';$('back-ai').href=data.return_url;$('balance').textContent=money(data.balance_fen);if(poll)clearTimeout(poll);if(data.status==='pending')poll=setTimeout(()=>{if(!document.hidden)loadCheckout().catch(e=>notice(e.message,'error'))},5000)}
async function refresh(){if(!token){expired();return}const info=await request('/api/v1/user/new-api/info');$('login').hidden=true;$('account').hidden=false;$('identity').textContent=info.email+' · #'+info.user_id;$('balance').textContent=money(info.balance_fen);$('open-ai').href=info.api_url+'/login';$('authorize-area').hidden=!bootstrap.oauth;$('home-actions').hidden=!!bootstrap.oauth;$('guide').hidden=!!bootstrap.oauth;await loadHistory();await loadCheckout()}
$('login-form').addEventListener('submit',event=>{event.preventDefault();busy('login-button',async()=>{const data=await request('/api/v1/new-api/portal/login',{email:$('email').value,password:$('password').value},false);$('password').value='';saveToken(data.token);notice('');await refresh()})});
$('authorize').onclick=()=>busy('authorize',async()=>{const data=await request('/api/v1/user/new-api/authorize',bootstrap.oauth);location.assign(data.redirect_url)});
$('logout').onclick=()=>busy('logout',async()=>{try{await request('/api/v1/user/new-api/logout',{})}finally{expired();notice('已退出此付款页面')}});
$('pay').onclick=()=>busy('pay',async()=>{await request('/api/v1/user/new-api/pay',{checkout_id:bootstrap.checkout});notice('本站扣款已提交，正在核对到账结果。请勿重新发起另一笔充值。','success');await refresh()});
$('refresh-pay').onclick=()=>busy('refresh-pay',loadCheckout);$('reload').onclick=()=>busy('reload',refresh);$('prev').onclick=()=>busy('prev',async()=>{page=Math.max(1,page-1);await loadHistory()});$('next').onclick=()=>busy('next',async()=>{page++;await loadHistory()});
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&token&&bootstrap.checkout)loadCheckout().catch(e=>notice(e.message,'error'))});
(async()=>{if(!bootstrap.info.enabled){$('disabled').hidden=false;return}const hash=new URLSearchParams(location.hash.slice(1)),ticket=hash.get('ticket');if(ticket){history.replaceState(null,'',location.pathname+location.search);const data=await request('/api/v1/new-api/portal/exchange',{ticket},false);saveToken(data.token)}await refresh()})().catch(e=>{notice(e.message,'error');if(!token)expired()});
</script></body></html>`))
