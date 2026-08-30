package autodl

import "testing"

func TestParseSSHLoginCommandAcceptsSupportedForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  SSHLoginTarget
	}{
		{
			name:  "AutoDL command",
			input: "ssh -p 16109 root@connect.westb.seetacloud.com",
			want:  SSHLoginTarget{Host: "connect.westb.seetacloud.com", Port: 16109, User: "root"},
		},
		{
			name:  "user option before port",
			input: "ssh -l operator -p 23456 gpu.example.com",
			want:  SSHLoginTarget{Host: "gpu.example.com", Port: 23456, User: "operator"},
		},
		{
			name:  "port before user option",
			input: "ssh -p 23456 -l operator gpu.example.com",
			want:  SSHLoginTarget{Host: "gpu.example.com", Port: 23456, User: "operator"},
		},
		{
			name:  "default port",
			input: "ssh root@192.0.2.10",
			want:  SSHLoginTarget{Host: "192.0.2.10", Port: 22, User: "root"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSSHLoginCommand(tt.input)
			if err != nil {
				t.Fatalf("ParseSSHLoginCommand() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseSSHLoginCommand() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSSHLoginCommandRejectsExecutableAndExpansionSyntax(t *testing.T) {
	inputs := []string{
		"bash -c ssh root@gpu.example.com",
		"ssh -o ProxyCommand=evil root@gpu.example.com",
		"ssh -oProxyJump=jump root@gpu.example.com",
		"ssh -J jump root@gpu.example.com",
		"ssh -F config root@gpu.example.com",
		"ssh -i identity root@gpu.example.com",
		"ssh -v root@gpu.example.com",
		"ssh root@gpu.example.com | sh",
		"ssh root@gpu.example.com > output",
		"ssh root@gpu.example.com uptime",
		"ssh root@gpu.example.com; uptime",
		"ssh root@$(hostname)",
		"ssh root@`hostname`",
		"ssh root@gpu.example.com\nwhoami",
		"ssh root@gpu.example.com\x00whoami",
		"ssh root\\@gpu.example.com",
		"ssh 'root@gpu.example.com'",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseSSHLoginCommand(input); err == nil {
				t.Fatalf("ParseSSHLoginCommand() accepted %q", input)
			}
		})
	}
}

func TestParseSSHLoginCommandRejectsDuplicateOrConflictingOptions(t *testing.T) {
	inputs := []string{
		"ssh -p 22 -p 22 root@gpu.example.com",
		"ssh -p 22 -p 23 root@gpu.example.com",
		"ssh -l root -l root gpu.example.com",
		"ssh -l root root@gpu.example.com",
		"ssh root@gpu.example.com -p 22",
	}

	for _, input := range inputs {
		if _, err := ParseSSHLoginCommand(input); err == nil {
			t.Fatalf("ParseSSHLoginCommand() accepted %q", input)
		}
	}
}

func TestParseSSHLoginCommandRejectsInvalidTargetAndPort(t *testing.T) {
	inputs := []string{
		"",
		"ssh",
		"ssh -p 0 root@gpu.example.com",
		"ssh -p 65536 root@gpu.example.com",
		"ssh -p nope root@gpu.example.com",
		"ssh -p root@gpu.example.com",
		"ssh @gpu.example.com",
		"ssh root@",
		"ssh root@@gpu.example.com",
		"ssh root@https://gpu.example.com",
		"ssh root@../gpu.example.com",
		"ssh root@[2001:db8::1]",
		"ssh root@-gpu.example.com",
		"ssh root@gpu..example.com",
		"ssh bad/user@gpu.example.com",
		"ssh 1bad@gpu.example.com",
	}

	for _, input := range inputs {
		if _, err := ParseSSHLoginCommand(input); err == nil {
			t.Fatalf("ParseSSHLoginCommand() accepted %q", input)
		}
	}
}
