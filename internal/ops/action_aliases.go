package ops

// publicActionAliases maps the stable schema vocabulary to the older internal
// operation names. Keeping this translation at the dispatch boundary lets the
// implementation retain backwards-compatible aliases without leaking them
// into the public contract.
var publicActionAliases = map[string]map[string]string{
	"zcr521_app_list": {
		"list": "all",
	},
	"zcr521_app_info": {
		"get": "info",
	},
	"zcr521_app_manage": {
		"launch": "start",
	},
	"zcr521_root_info": {
		"detect":       "get",
		"capabilities": "get",
		"self_test":    "check",
	},
	"zcr521_root_module": {
		"remove": "delete",
		"logs":   "log",
	},
	"zcr521_systemless": {
		"apply": "stage",
	},
	"zcr521_property": {
		"reset": "delete",
	},
	"zcr521_audio": {
		"volume": "set_volume",
	},
	"zcr521_connectivity": {
		"get":           "status",
		"airplane_mode": "airplane",
	},
	"zcr521_input_method": {
		"get": "current",
	},
	"zcr521_accessibility": {
		"list": "get",
	},
	"zcr521_network": {
		"ports":        "port_check",
		"wifi":         "wifi_info",
		"connectivity": "internet_test",
	},
	"zcr521_log": {
		"mcp": "service",
	},
	"zcr521_diagnostics": {
		"self_test": "check",
		"collect":   "report",
	},
	"zcr521_backup": {
		"remove": "delete",
	},
}

func normalizePublicRequest(req Request) Request {
	rawAction, ok := req.Args["action"].(string)
	if !ok {
		return req
	}
	action := normalizeTool(rawAction)
	aliases := publicActionAliases[req.Tool]
	target, exists := aliases[action]
	if !exists {
		return req
	}
	cloned := make(map[string]any, len(req.Args))
	for key, value := range req.Args {
		cloned[key] = value
	}
	cloned["action"] = target
	req.Args = cloned
	return req
}
