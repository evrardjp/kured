package main

import (
	"reflect"
	"testing"

	log "github.com/sirupsen/logrus"
	assert "gotest.tools/v3/assert"
)

func Test_buildHostCommand(t *testing.T) {
	type args struct {
		command []string
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "Ensure command will run with nsenter",
			args: args{command: []string{"ls", "-Fal"}},
			want: []string{"/usr/bin/nsenter", "-m/proc/1/ns/mnt", "--", "ls", "-Fal"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildHostCommand(tt.args.command); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildHostCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_buildSentinelCommand(t *testing.T) {
	type args struct {
		rebootSentinelFile    string
		rebootSentinelCommand string
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "Ensure a sentinelFile generates a shell 'test' command with the right file",
			args: args{
				rebootSentinelFile:    "/test1",
				rebootSentinelCommand: "",
			},
			want: []string{"test", "-f", "/test1"},
		},
		{
			name: "Ensure a sentinelCommand has priority over a sentinelFile if both are provided (because sentinelFile is always provided)",
			args: args{
				rebootSentinelFile:    "/test1",
				rebootSentinelCommand: "/sbin/reboot-required -r",
			},
			want: []string{"/sbin/reboot-required", "-r"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSentinelCommand(tt.args.rebootSentinelFile, tt.args.rebootSentinelCommand); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseSentinelCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseRebootCommand(t *testing.T) {
	type args struct {
		rebootCommand string
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "Ensure a reboot command is properly parsed",
			args: args{
				rebootCommand: "/sbin/systemctl reboot",
			},
			want: []string{"/sbin/systemctl", "reboot"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRebootCommand(tt.args.rebootCommand); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseRebootCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_rebootRequired(t *testing.T) {
	type args struct {
		sentinelCommand []string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Ensure rc = 0 means reboot required",
			args: args{
				sentinelCommand: []string{"true"},
			},
			want: true,
		},
		{
			name: "Ensure rc != 0 means reboot NOT required",
			args: args{
				sentinelCommand: []string{"false"},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rebootRequired(tt.args.sentinelCommand); got != tt.want {
				t.Errorf("rebootRequired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_rebootRequired_fatals(t *testing.T) {
	cases := []struct {
		param       []string
		expectFatal bool
	}{
		{
			param:       []string{"true"},
			expectFatal: false,
		},
		{
			param:       []string{"./babar"},
			expectFatal: true,
		},
	}

	defer func() { log.StandardLogger().ExitFunc = nil }()
	var fatal bool
	log.StandardLogger().ExitFunc = func(int) { fatal = true }

	for _, c := range cases {
		fatal = false
		rebootRequired(c.param)
		assert.Equal(t, c.expectFatal, fatal)
	}

}
