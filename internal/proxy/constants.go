package proxy

// Endpoints is the set of request paths (exactly as llama-swap sends them)
// that loopguard intercepts for loop-detection, per specification §2.2
// ("対象とする生成系エンドポイントは以下の4つに固定する").
//
// Any path/method combination NOT listed here is passed through verbatim
// via httputil.ReverseProxy.
var Endpoints = []endpoint{
	{Method: "POST", Path: "/completion"},
	{Method: "POST", Path: "/v1/completions"},
	{Method: "POST", Path: "/v1/chat/completions"},
	{Method: "POST", Path: "/infill"},
}

// endpointer matches an HTTP request to one of the generation endpoints.
type endpoint struct {
	Method string
	Path   string
}

// IsGenerated reports whether the request matches a loop-guarded endpoint.
func IsGenerated(method, path string) bool {
	for _, e := range Endpoints {
		if e.Method == method && e.Path == path {
			return true
		}
	}
	return false
}
