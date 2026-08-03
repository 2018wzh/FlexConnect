package vpn

import "testing"

func TestEffectiveMTUUsesLowerServerOrProfileLimit(t *testing.T) {
	tests := []struct {
		name    string
		server  int
		profile int
		want    int
		wantErr bool
	}{
		{name: "server lower", server: 1271, profile: 1399, want: 1271},
		{name: "profile lower", server: 1399, profile: 1300, want: 1300},
		{name: "server missing", server: 0, profile: 1300, want: 1300},
		{name: "profile default", server: 0, profile: 0, want: defaultMTU},
		{name: "invalid profile", server: 1399, profile: 500, wantErr: true},
		{name: "invalid server", server: 500, profile: 1399, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := effectiveMTU(test.server, test.profile)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("MTU = %d, want %d", got, test.want)
			}
		})
	}
}
