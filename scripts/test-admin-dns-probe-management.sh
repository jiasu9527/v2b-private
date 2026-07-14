#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
page="$root/admin-src/src/pages/DNSFailoverPage.tsx"

grep -q '探针接入地址' "$page"
grep -q '查看安装命令' "$page"
grep -q '删除探针' "$page"
grep -q 'apiDelete(`${BASE}/probes/${p.id}`)' "$page"
grep -q 'apiPut(`${BASE}/settings`' "$page"
grep -q "apiGet('/dns/domain/list'" "$page"
grep -q "apiGet('/dns/record/list'" "$page"
grep -q 'label="选择 DNSPod 域名"' "$page"
grep -q 'label="选择解析记录"' "$page"
grep -q 'failure_threshold:3,success_threshold:6,single_probe_failure_threshold:5,single_probe_success_threshold:8' "$page"
if grep -q 'label="check_host"' "$page"; then
  echo "DNS failover target form must derive check_host from dns_value" >&2
  exit 1
fi
grep -q '监控详情' "$page"
grep -q 'consecutive_failure' "$page"
grep -q 'last_reported_at' "$page"
grep -q 'failure_threshold_pending' "$page"
grep -q "label:'诊断日志'" "$page"
grep -q 'stage:logStage,level:logLevel,outcome:logOutcome' "$page"
grep -q 'placeholder="全部阶段"' "$page"
grep -q 'placeholder="全部级别"' "$page"
grep -q 'placeholder="全部结果"' "$page"
grep -q "title:'阶段'" "$page"
grep -q "title:'级别'" "$page"
grep -q "title:'消息'" "$page"
grep -q "title:'详情'" "$page"
grep -q 'latency_ms' "$page"
grep -q 'resolved_ip' "$page"
grep -q '数据过期' "$page"
grep -q 'x.stale' "$page"
grep -q 'decision_available' "$page"
grep -q '无新鲜测活' "$page"
grep -q "rule_disabled:'规则已停用'" "$page"
grep -q "error:'请求失败'" "$page"
grep -q "if(tab!=='logs')return" "$page"
grep -q "snapshot:'评估快照'" "$page"
grep -q "threshold_pending:'等待阈值'" "$page"
grep -q "no_data:'无新鲜数据'" "$page"
grep -q "claimed:'开始处理'" "$page"
if grep -q "stage:'probe_result'" "$page"; then
  echo "DNS failover diagnostic log must not force the probe_result stage" >&2
  exit 1
fi
