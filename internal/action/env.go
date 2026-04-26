package action

import "strings"

type env map[string]string

func newEnv(values []string) env {
	out := make(env, len(values))
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if ok {
			out[key] = val
		}
	}
	return out
}

func (e env) get(key string) string {
	return e[key]
}

func (e env) input(name string) string {
	upper := strings.ToUpper(name)
	candidates := []string{
		"INPUT_" + upper,
		"INPUT_" + strings.ReplaceAll(upper, "-", "_"),
	}
	for _, candidate := range candidates {
		if value := strings.TrimSpace(e[candidate]); value != "" {
			return value
		}
	}
	return ""
}
