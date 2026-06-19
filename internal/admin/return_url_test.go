package admin

import "testing"

func TestBuildReturnURL(t *testing.T) {
	cases := []struct {
		name      string
		base      string
		projectID string
		want      string
	}{
		{
			name:      "empty base returns empty (gateways without success_url requirement still work)",
			base:      "",
			projectID: "404ed32e-5585-4fec-8c30-1797ad9f5f33",
			want:      "",
		},
		{
			name:      "base without trailing slash",
			base:      "https://code.cs.ac.cn/projects",
			projectID: "404ed32e-5585-4fec-8c30-1797ad9f5f33",
			want:      "https://code.cs.ac.cn/projects/404ed32e-5585-4fec-8c30-1797ad9f5f33/subscription",
		},
		{
			name:      "base with trailing slash is normalized (no //)",
			base:      "https://code.cs.ac.cn/projects/",
			projectID: "404ed32e-5585-4fec-8c30-1797ad9f5f33",
			want:      "https://code.cs.ac.cn/projects/404ed32e-5585-4fec-8c30-1797ad9f5f33/subscription",
		},
		{
			name:      "base with multiple trailing slashes is normalized",
			base:      "https://code.cs.ac.cn/projects///",
			projectID: "404ed32e-5585-4fec-8c30-1797ad9f5f33",
			want:      "https://code.cs.ac.cn/projects/404ed32e-5585-4fec-8c30-1797ad9f5f33/subscription",
		},
		{
			name:      "base may be the bare host",
			base:      "https://code.cs.ac.cn",
			projectID: "abc",
			want:      "https://code.cs.ac.cn/abc/subscription",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildReturnURL(tc.base, tc.projectID)
			if got != tc.want {
				t.Errorf("buildReturnURL(%q, %q) = %q, want %q", tc.base, tc.projectID, got, tc.want)
			}
		})
	}
}
