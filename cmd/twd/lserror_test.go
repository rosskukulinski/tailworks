package main

import "testing"

func TestDeployErrorLine(t *testing.T) {
	bind := "Socket \"ingress\": bind(): Address already in use (os error 48)"
	for in, want := range map[string]string{
		"workerd exited 6 times: " + bind + "\n" + bind + "\n" + bind: "workerd exited 6 times: " + bind + " · twd logs app --console",
		"boom\nat x\nat y": "boom (+2 more lines) · twd logs app --console",
	} {
		if got := deployErrorLine("app", in); got != want {
			t.Errorf("deployErrorLine(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
