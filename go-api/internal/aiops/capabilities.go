package aiops

type capabilityDomain string

const (
	domainPanel                capabilityDomain = "panel_status"
	domainUsers                capabilityDomain = "users"
	domainOrders               capabilityDomain = "orders"
	domainNodes                capabilityDomain = "nodes"
	domainEntries              capabilityDomain = "entries"
	domainTickets              capabilityDomain = "tickets"
	domainServers              capabilityDomain = "servers"
	domainPaymentConfiguration capabilityDomain = "payment_configuration"
)

type toolCapability struct {
	Domain     capabilityDomain
	ActionKind string
}

// This catalog is the capability boundary for panel tools. Adding a schema or
// handler alone cannot grant a new domain. SSH remains the authority of its
// configured remote account, not a filesystem sandbox enforced by this catalog.
var toolCapabilities = map[string]toolCapability{
	"panel_status":               {domainPanel, ""},
	"user_lookup":                {domainUsers, ""},
	"user_detail":                {domainUsers, ""},
	"plans_list":                 {domainUsers, ""},
	"groups_list":                {domainUsers, ""},
	"propose_user_update":        {domainUsers, "user_update"},
	"orders_list":                {domainOrders, ""},
	"nodes_list":                 {domainNodes, ""},
	"node_detail":                {domainNodes, ""},
	"propose_node_change":        {domainNodes, "node_change"},
	"propose_node_update":        {domainNodes, "node_update"},
	"entry_rules_list":           {domainEntries, ""},
	"entry_simulate":             {domainEntries, ""},
	"entry_health":               {domainEntries, ""},
	"backup_ips_list":            {domainEntries, ""},
	"propose_entry_host":         {domainEntries, "entry_host"},
	"monitor_run":                {domainEntries, "monitor_run"},
	"entry_probe_targets":        {domainEntries, ""},
	"entry_probe_run":            {domainEntries, "entry_probe_run"},
	"entry_probe_result":         {domainEntries, ""},
	"entry_rule_detail":          {domainEntries, ""},
	"propose_entry_rule_update":  {domainEntries, "entry_rule_update"},
	"entry_split_options":        {domainEntries, ""},
	"propose_entry_manual_split": {domainEntries, "entry_manual_split"},
	"entry_split_operation":      {domainEntries, ""},
	"propose_entry_auto_split":   {domainEntries, "entry_auto_split"},
	"propose_entry_split":        {domainEntries, "entry_split"},
	"tickets_list":               {domainTickets, ""},
	"ticket_get":                 {domainTickets, ""},
	"ticket_message_get":         {domainTickets, ""},
	"ticket_settings":            {domainTickets, ""},
	"propose_ticket_update":      {domainTickets, "ticket_update"},
	"propose_ticket_settings":    {domainTickets, "ticket_settings"},
	"servers_list":               {domainServers, ""},
	"ssh_status":                 {domainServers, "ssh_command"},
	"propose_ssh_command":        {domainServers, "ssh_command"},
	"ssh_server_register":        {domainServers, "server_register"},
}

var actionCapabilities = map[string]capabilityDomain{
	"user_update":        domainUsers,
	"node_change":        domainNodes,
	"node_update":        domainNodes,
	"entry_host":         domainEntries,
	"monitor_run":        domainEntries,
	"entry_probe_run":    domainEntries,
	"entry_rule_update":  domainEntries,
	"entry_manual_split": domainEntries,
	"entry_auto_split":   domainEntries,
	"entry_split":        domainEntries,
	"ticket_update":      domainTickets,
	"ticket_settings":    domainTickets,
	"ssh_command":        domainServers,
	"server_register":    domainServers,
}

func capabilityDomainAllowed(domain capabilityDomain) bool {
	switch domain {
	case domainPanel, domainUsers, domainOrders, domainNodes, domainEntries, domainTickets, domainServers:
		return true
	case domainPaymentConfiguration:
		return false
	default:
		return false
	}
}

func toolCapabilityAllowed(name string) bool {
	capability, exists := toolCapabilities[name]
	if !exists || !capabilityDomainAllowed(capability.Domain) {
		return false
	}
	return capability.ActionKind == "" || (actionCapabilities[capability.ActionKind] == capability.Domain && actionCapabilityAllowed(capability.ActionKind))
}

func actionCapabilityAllowed(kind string) bool {
	domain, exists := actionCapabilities[kind]
	return exists && capabilityDomainAllowed(domain)
}

func permittedToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if toolCapabilityAllowed(definition.Function.Name) {
			out = append(out, definition)
		}
	}
	return out
}
