package svcbind

import "testing"

func TestFromWrangler(t *testing.T) {
	bs, err := FromWrangler([]byte(`{"services":[{"binding":"AUTH","service":"auth"},{"binding":"ADMIN","service":"auth","entrypoint":"Admin"}]}`))
	if err != nil || len(bs) != 2 || bs[1].Entrypoint != "Admin" || Find(bs, "AUTH").Service != "auth" || Find(bs, "X") != nil {
		t.Fatal(bs, err)
	}
	if bs, err := FromWrangler([]byte(`{}`)); bs != nil || err != nil {
		t.Fatal(bs, err)
	}
	for _, bad := range []string{
		`{"services":[{"binding":"1x","service":"a"}]}`,
		`{"services":[{"binding":"__TW_X","service":"a"}]}`,
		`{"services":[{"binding":"A","service":"a"},{"binding":"A","service":"b"}]}`,
		`{"services":[{"binding":"A","service":"Not_A_Label"}]}`,
		`{"services":[{"binding":"A","service":"a","entrypoint":"default"}]}`,
		`{"services":[{"binding":"A","service":"a","entrypoint":"__tw_binding"}]}`,
		`{"services":[{"binding":"A","service":"a","environment":"staging"}]}`,
		`{"services":[{"binding":"A","service":"a","props":{"x":1}}]}`,
	} {
		if _, err := FromWrangler([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestValidMethod(t *testing.T) {
	for _, m := range []string{"add", "getUser", "_x", "$y"} {
		if !ValidMethod(m) {
			t.Errorf("refused %s", m)
		}
	}
	for _, m := range []string{"", "fetch", "constructor", "__tw_kill", "then", "1a", "a-b"} {
		if ValidMethod(m) {
			t.Errorf("accepted %q", m)
		}
	}
}
