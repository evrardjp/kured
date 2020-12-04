package main

import (
	"testing"
)

func Test_rebootRequired(t *testing.T) {
	type args struct {
		testCommand []string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "if the test command for sentinel is true, reboot should be required",
			args: args{testCommand: []string{"true"}},
			want: true,
		},
		{
			name: "if the test command for sentinel is not succeeding, reboot should be prevented",
			args: args{testCommand: []string{"cat", "test"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rebootRequired(tt.args.testCommand...); got != tt.want {
				t.Errorf("rebootRequired() = %v, want %v", got, tt.want)
			}
		})
	}
}
