package chatagent

import "testing"

func TestRequestReasoningEffort(t *testing.T) {
	for _, effort := range []string{"", "xhigh", "none"} {
		t.Run(effort, func(t *testing.T) {
			root, tree, _, _ := repoTree(t)
			m := &model{}
			_, _, err := review(t, root, tree, m, func(cfg *Config) { cfg.Effort = effort })
			if err != nil {
				t.Fatal(err)
			}
			body := m.got()[0]
			if effort == "" {
				if _, ok := body["reasoning"]; ok {
					t.Fatal(body)
				}
				return
			}
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != effort {
				t.Fatal(body)
			}
		})
	}
}
