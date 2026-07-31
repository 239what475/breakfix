package generator

import "testing"

func TestBackendPathMapsOnlyWorkspaceVirtualRoot(t *testing.T) {
	cases := []struct {
		path      string
		directory bool
		want      string
		wantErr   bool
	}{
		{path: "", directory: true, want: "."},
		{path: "/workspace", directory: true, want: "."},
		{path: "/workspace/", directory: true, want: "."},
		{path: "/workspace/challenge.yaml", want: "./challenge.yaml"},
		{path: "/workspace/nodes/host/checks.sh", want: "./nodes/host/checks.sh"},
		{path: "challenge.yaml", want: "./challenge.yaml"},
		{path: "/etc/passwd", wantErr: true},
		{path: "/workspace/../etc/passwd", wantErr: true},
		{path: "/workspace", wantErr: true},
	}
	for _, test := range cases {
		got, err := backendPath(test.path, test.directory)
		if test.wantErr {
			if err == nil {
				t.Fatalf("backendPath(%q, %t) = %q, want error", test.path, test.directory, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("backendPath(%q, %t) = %q, %v; want %q", test.path, test.directory, got, err, test.want)
		}
	}
}
