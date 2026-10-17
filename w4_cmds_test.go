package main

import "testing"

func TestThemeMenu(t *testing.T) {
	m := slashMenu("/theme ")
	if m == nil {
		t.Fatal("no /theme menu")
	}
	seen := map[string]bool{}
	for _, it := range m.Items {
		seen[it.Insert] = it.Submit
	}
	for _, n := range []string{"blaze", "ember", "mint", "violet", "mono"} {
		if !seen["/theme "+n] {
			t.Errorf("/theme %s missing or not runnable", n)
		}
	}
	if m := slashMenu("/theme em"); m == nil || len(m.Items) == 0 || m.Items[0].Insert != "/theme ember" {
		t.Error("typing filters the presets")
	}
}
