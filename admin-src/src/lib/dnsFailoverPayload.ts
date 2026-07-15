type RuleTargetFormValue = {
  id?: number | string;
  name?: string;
  dns_type?: string;
  dns_value?: string;
  check_port?: number;
  enabled?: boolean;
};

/**
 * Convert Ant Design form state into the strict DNS failover write contract.
 *
 * Rule list responses contain read-only database fields such as `group_id`,
 * `created_at`, and `updated_at`. Form state can retain those fields after an
 * edit, so never spread a response object into a write request.
 */
export function buildDNSFailoverRulePayload(value: Record<string, any>) {
  const targets = Array.isArray(value.targets)
    ? value.targets.map((target: RuleTargetFormValue, index: number) => {
        const result: Record<string, any> = {
          sort: index,
          name: target?.name,
          dns_type: target?.dns_type,
          dns_value: target?.dns_value,
          check_port: target?.check_port,
          enabled: target?.enabled,
        };
        if (target?.id !== undefined && target.id !== null && target.id !== '') {
          const id = Number(target.id);
          if (Number.isSafeInteger(id) && id > 0) result.id = id;
        }
        return result;
      })
    : [];

  return {
    name: value.name,
    domain_id: value.domain_id,
    domain: value.domain,
    record_id: value.record_id,
    subdomain: value.subdomain,
    record_line_id: value.record_line_id,
    record_line_name: value.record_line_name,
    ttl: value.ttl,
    mx: value.mx,
    weight: value.weight,
    enabled: value.enabled,
    auto_failback: value.auto_failback,
    check_interval_sec: value.check_interval_sec,
    tcp_timeout_ms: value.tcp_timeout_ms,
    failure_threshold: value.failure_threshold,
    success_threshold: value.success_threshold,
    single_probe_failure_threshold: value.single_probe_failure_threshold,
    single_probe_success_threshold: value.single_probe_success_threshold,
    probe_offline_sec: value.probe_offline_sec,
    cooldown_sec: value.cooldown_sec,
    targets,
    probe_ids: Array.isArray(value.probe_ids) ? value.probe_ids : [],
  };
}
