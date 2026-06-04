package main

import (
	"errors"
	"testing"
)

func TestShouldRunHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		applyOnly  bool
		httpServer bool
		err        error
		want       bool
	}{
		{
			name:      "default apply only",
			applyOnly: true,
			want:      false,
		},
		{
			name:       "http flag starts server",
			applyOnly:  true,
			httpServer: true,
			want:       true,
		},
		{
			name:      "legacy apply only false starts server",
			applyOnly: false,
			want:      true,
		},
		{
			name:       "migration failure does not start server",
			applyOnly:  false,
			httpServer: true,
			err:        errors.New("migration failed"),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := shouldRunHTTP(tt.applyOnly, tt.httpServer, tt.err)
			if got != tt.want {
				t.Fatalf("shouldRunHTTP(%t, %t, %v) = %t, want %t", tt.applyOnly, tt.httpServer, tt.err, got, tt.want)
			}
		})
	}
}
